/*
Copyright 2023 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"

	machineapiv1beta1 "github.com/openshift/api/machine/v1beta1"
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/apimachinery/pkg/labels"
	metricsapi "k8s.io/metrics/pkg/apis/metrics"
	metricsV1beta1api "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

const (
	MASTER_NODE_LABEL = "node-role.kubernetes.io/master"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}
var config *restclient.Config
var kubeconfig *string
var clientset *kubernetes.Clientset

func main() {
	var err error
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	klog.InitFlags(nil)

	flag.Parse()

	// use the current context in kubeconfig
	config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {

		config, err = restclient.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
	}

	// create the clientset
	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	klog.Infof("Fitting model to sample data...")
	for fit_model() != nil {
		klog.Infof("Waiting for model predictor to be ready")
		<-time.After(5 * time.Second)
	}
	for {
		err := runScaler()
		if err != nil {
			klog.Errorf("There was an incident: %s", err)
		}
		<-time.After(30 * time.Second)
	}

}

func runScaler() error {
	nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	klog.InfoS("Checking for total nodes in cluster", "nodes", len(nodes.Items))
	computeNodes := []corev1.Node{}
	for _, node := range nodes.Items {
		if _, found := node.Labels[MASTER_NODE_LABEL]; !found {
			computeNodes = append(computeNodes, node)
		}
	}

	resourceCapacityTotals := corev1.ResourceList{}
	for _, node := range computeNodes {
		for resource, value := range node.Status.Capacity {
			if current, ok := resourceCapacityTotals[resource]; ok {
				resourceCapacityTotals[resource] = sumQuantity(current, value)
			} else {
				resourceCapacityTotals[resource] = value
			}
		}
	}

	klog.InfoS("Filtering out compute nodes from total", "nodes", len(computeNodes))
	if value, found := resourceCapacityTotals[corev1.ResourceCPU]; found {
		klog.InfoS("CPU capacity for compute nodes", "value", value.String())
	} else {
		klog.Error("No value for CPU capacity, this should not happen.")
	}
	if value, found := resourceCapacityTotals[corev1.ResourceMemory]; found {
		klog.InfoS("Memory resource capacity for compute nodes", "value", value.String())
	} else {
		klog.Error("No value for Memory capacity, this should not happen.")
	}

	// TODO
	// 1. get machinesets to create a list of possible machine sizes to create
	// 2. add call to prediction service
	// 3. calculate difference between predicted load and actual capacity
	// 4. do something:
	//   4.1 if predicted load > actual capacity then scale out
	//   4.2 if predicted load < actual capacity then scale in

	machineClientset, err := machinev1beta1.NewForConfig(config)
	if err != nil {
		return err
	}

	namespace := "openshift-machine-api"
	machineSets, err := machineClientset.MachineSets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 1. We're gonna get the machineset, but assume the values for now
	var useMachineSet *machineapiv1beta1.MachineSet
	for _, machineset := range machineSets.Items {
		useMachineSet = &machineset
		break
	}

	var nodeCPU = resource.MustParse("4")
	var nodeMemory = resource.MustParse("16Gi")
	//var nodeCreationDelay = 8 * time.Minute

	klog.InfoS("Each node we have has these resources", "cpu", nodeCPU.String(), "memory", nodeMemory.String())
	totalMemory := resourceCapacityTotals[corev1.ResourceMemory]
	totalCPU := resourceCapacityTotals[corev1.ResourceCPU]

	// Memory here is in megabytes for easier prediction
	predictedCPU, predictedMemory, err := predict(time.Now().Add(20*time.Minute).UTC(), totalCPU.MilliValue(), totalMemory.Value()/(1024*1024))
	if err != nil {
		return err
	}

	// See what the % change was just for fun
	klog.Infof("Prediction: CPU: %d MEM: %d", predictedCPU, predictedMemory)
	cpuDeltaPrc := float64(predictedCPU) / float64(totalCPU.MilliValue())
	memoryDeltaPrc := float64(predictedMemory) / float64(totalMemory.Value()/(1024*1024))
	klog.Infof("MEMPRC: %d%% CPUPRC: %d%%\t", int64(memoryDeltaPrc)*100, int64(cpuDeltaPrc)*100)

	// Figure out how much CPU and memory we lack
	cpuDelta := predictedCPU - totalCPU.MilliValue()
	memoryDelta := predictedMemory - (totalMemory.Value() / (1024 * 1024))

	klog.Infof("CPU Delta is %d", cpuDelta)
	klog.Infof("Memory Delta is %d", memoryDelta)

	klog.Infof("Each new node will have: CPU: %d MEM: %d", nodeCPU.MilliValue(), nodeMemory.Value()/(1024*1024))

	// Figure out how many nodes that translates to using our node size from above
	numNodeCpuDelta := cpuDelta / nodeCPU.MilliValue()
	numNodeMemDelta := memoryDelta / (nodeMemory.Value() / (1024 * 1024))

	klog.Infof("CPU thinks it needs %d more nodes ", numNodeCpuDelta)
	klog.Infof("Memory thinks it needs %d more nodes", numNodeMemDelta)

	// There can be nodes that are in progress so let's just count our compute nodes for now and ignore what the machienset has done
	var currentNodes, desiredNodes int32
	currentNodes = int32(len(computeNodes))
	desiredNodes = currentNodes + int32(numNodeCpuDelta)

	// If we need both more CPU and more Memory, make the hungriest one happy
	if numNodeMemDelta > numNodeCpuDelta {
		desiredNodes = currentNodes + int32(numNodeMemDelta)
	}

	klog.Infof("I want to scale machineset %s to %d desired nodes (currently %d)", useMachineSet.Name, desiredNodes, *useMachineSet.Spec.Replicas)

	// TODO(jkyros): we can't just delete the nodes if they're ful, regardless of our prediction,
	// so we need to check the current usage

	metricsClient, err := metricsclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	selector, err := labels.Parse("!node-role.kubernetes.io/master")
	metrics, err := getNodeMetricsFromMetricsAPI(metricsClient, "", selector)
	if err != nil {
		return err
	}

	resourceUsageTotals := corev1.ResourceList{}
	for _, node := range computeNodes {
		// TODO(jkyros): eew, I know
		for _, metric := range metrics.Items {
			memory := metric.Usage[corev1.ResourceMemory]
			cpu := metric.Usage[corev1.ResourceCPU]
			if metric.Name == node.Name {
				klog.Infof("Collecting usage %s %s %s", metric.Name, memory.String(), cpu.String())
				for resource, value := range metric.Usage {
					if current, ok := resourceUsageTotals[resource]; ok {
						resourceUsageTotals[resource] = sumQuantity(current, value)
					} else {
						resourceUsageTotals[resource] = value
					}

				}
			}
		}
	}

	usedMemory := resourceUsageTotals[corev1.ResourceMemory]
	usedCPU := resourceUsageTotals[corev1.ResourceCPU]

	klog.Infof("Total usage CPU: %d MEM: %d", usedCPU.MilliValue(), usedMemory.Value()/(1024*1024))

	// TODO(jkyros): we really need to look at the delta here, but we need to not scale below what we're using
	if desiredNodes < currentNodes {
		// If what we're using is more than predicted, and we were going to scale down
		if usedCPU.MilliValue() > predictedCPU || usedMemory.Value()/(1024*1024) > predictedMemory {
			klog.Infof("Resource consumption exceeds prediction, can't scale down yet")
		}
	}

	// TODO(jkyros): If we don't wait for the nodes to be ready, things get weird, e.g. something like this shows in our resource tally :
	// ip-10-0-13-244.ec2.internal   NotReady                      worker                 46s   v1.28.3+4cbdd29
	// ip-10-0-16-186.ec2.internal   Ready                         worker                 13m   v1.28.3+4cbdd29
	// ip-10-0-17-100.ec2.internal   Ready                         control-plane,master   9h    v1.28.3+4cbdd29
	// ip-10-0-21-160.ec2.internal   NotReady,SchedulingDisabled   worker                 38s   v1.28.3+4cbdd29
	// ip-10-0-21-70.ec2.internal    Ready,SchedulingDisabled      worker                 14m   v1.28.3+4cbdd29
	// ip-10-0-41-29.ec2.internal    Ready                         control-plane,master   9h    v1.28.3+4cbdd29
	// ip-10-0-42-203.ec2.internal   Ready                         worker                 9h    v1.28.3+4cbdd29
	// ip-10-0-43-226.ec2.internal   Ready                         worker                 53m   v1.28.3+4cbdd29
	// ip-10-0-48-62.ec2.internal    Ready                         worker                 14m   v1.28.3+4cbdd29
	// ip-10-0-5-106.ec2.internal    Ready,SchedulingDisabled      worker                 51s   v1.28.3+4cbdd29
	// ip-10-0-50-89.ec2.internal    NotReady                      worker                 40s   v1.28.3+4cbdd29
	// ip-10-0-54-130.ec2.internal   Ready                         control-plane,master   9h    v1.28.3+4cbdd29
	// ip-10-0-56-141.ec2.internal   NotReady,SchedulingDisabled   worker                 49s   v1.28.3+4cbdd29
	// ip-10-0-57-72.ec2.internal    Ready                         worker                 53m   v1.28.3+4cbdd29
	// ip-10-0-61-86.ec2.internal    Ready                         worker                 62s   v1.28.3+4cbdd29

	if currentNodes == desiredNodes {
		klog.Infof("Machineset %s replicas is already set to %d", useMachineSet.Name, desiredNodes)
	} else {
		klog.Infof("Scaling machineset %s to %d desired nodes (currently %d ready %d)", useMachineSet.Name, desiredNodes, *useMachineSet.Spec.Replicas, useMachineSet.Status.ReadyReplicas)

		// TODO(jkyros): Yeah, I know this isn't a proper reconciliation, someone could beat us
		modifyMachineSet := useMachineSet.DeepCopy()
		modifyMachineSet.Spec.Replicas = &desiredNodes
		_, err = machineClientset.MachineSets(namespace).Update(context.TODO(), modifyMachineSet, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		<-time.After(30 * time.Second)

	}
	print_machinesets(config)
	return nil
}

func sumQuantity(left, right resource.Quantity) resource.Quantity {
	result := resource.Quantity{}
	result.Add(left)
	result.Add(right)
	return result
}

func print_machines(config *restclient.Config) error {
	clientset, err := machinev1beta1.NewForConfig(config)
	if err != nil {
		return err
	}

	namespace := "openshift-machine-api"
	machines, err := clientset.Machines(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("There are %d machines in the cluster\n", len(machines.Items))

	for _, machine := range machines.Items {
		fmt.Printf("  %s\n", machine.Name)
	}
	return nil
}

func print_machinesets(config *restclient.Config) error {
	clientset, err := machinev1beta1.NewForConfig(config)
	if err != nil {
		return err
	}

	namespace := "openshift-machine-api"
	machineSets, err := clientset.MachineSets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("There are %d machinesets in the cluster\n", len(machineSets.Items))
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(machineapiv1beta1.SchemeGroupVersion,
		&machineapiv1beta1.AWSMachineProviderConfig{},
	)
	codecFactory := serializer.NewCodecFactory(scheme)
	decoder := codecFactory.UniversalDecoder(machineapiv1beta1.GroupVersion)

	for _, machineset := range machineSets.Items {
		fmt.Printf("  %s\n", machineset.Name)
		obj, err := runtime.Decode(decoder, machineset.Spec.Template.Spec.ProviderSpec.Value.Raw)
		if err != nil {
			return err
		}
		switch obj := obj.(type) {
		case *machineapiv1beta1.AWSMachineProviderConfig:
			var awsProviderConfig machineapiv1beta1.AWSMachineProviderConfig
			awsProviderConfig = *obj
			fmt.Printf("  - %s\n", awsProviderConfig.InstanceType)
			// TODO(jkyros): I wonder if the cluster already know sthis:
			//Instance Size 	vCPU 	Memory (GiB) 	Instance Storage (GB) 	Network Bandwidth (Gbps) 	EBS Bandwidth (Gbps)
			//m6i.xlarge 	       4 	          16 	EBS-Only                           	Up to 12.5 	               Up to 10
		}

	}
	return nil
}

func getNodeMetricsFromMetricsAPI(metricsClient metricsclientset.Interface, resourceName string, selector labels.Selector) (*metricsapi.NodeMetricsList, error) {
	var err error
	versionedMetrics := &metricsV1beta1api.NodeMetricsList{}
	mc := metricsClient.MetricsV1beta1()
	nm := mc.NodeMetricses()
	if resourceName != "" {
		m, err := nm.Get(context.TODO(), resourceName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		versionedMetrics.Items = []metricsV1beta1api.NodeMetrics{*m}
	} else {
		versionedMetrics, err = nm.List(context.TODO(), metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return nil, err
		}
	}
	metrics := &metricsapi.NodeMetricsList{}
	err = metricsV1beta1api.Convert_v1beta1_NodeMetricsList_To_metrics_NodeMetricsList(versionedMetrics, metrics, nil)
	if err != nil {
		return nil, err
	}
	return metrics, nil
}

type PredictionResponse struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
}

// fit_model supplies data to the model esrver, for now
// it's just the fake data generated by the generator
func fit_model() error {
	var err error
	var dataDir = "data"
	// Data generator dumps some stuff in files
	// arranged by date, we just want to grab the one it spits out
	var dataFile *os.File
	for dataFile == nil {
		files, err := os.ReadDir(dataDir)
		if err != nil {
			return err
		}
		for _, file := range files {
			if strings.HasPrefix(file.Name(), "resource") && strings.HasSuffix(file.Name(), ".csv") {
				dataFile, err = os.Open(filepath.Join(dataDir, file.Name()))
				if err != nil {
					return err
				}
				break
			}
		}
	}
	klog.Infof("Feeding datafile %s to the model fitter", dataFile.Name())
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", filepath.Base(dataFile.Name()))
	io.Copy(part, dataFile)
	writer.Close()

	r, _ := http.NewRequest("POST", "http://localhost:5001/fit-model", body)
	r.Header.Add("Content-Type", writer.FormDataContentType())
	_, err = httpClient.Do(r)
	if err != nil {
		return err
	}
	return nil
}

func predict(t time.Time, currentCPU int64, currentMemory int64) (predictedCPU, predictedMemory int64, predictErr error) {

	req, err := http.NewRequest("GET", "http://localhost:5001/predict", nil)

	q := req.URL.Query()
	q.Add("type", "resource")
	q.Add("timestamp", t.Format("2006-01-02T15:04:05"))
	req.URL.RawQuery = q.Encode()

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	klog.Infof("REQUESTING: %s", req.URL.String())

	var prediction PredictionResponse
	err = json.NewDecoder(resp.Body).Decode(&prediction)
	if err != nil {
		return 0, 0, err
	}

	return prediction.CPU, prediction.Memory, nil
}

// This is a dummy prediction function that just lets us rock it back and forth, we'll swap this in for a
// proper prediction model/api call later
func predictfake(t time.Time, currentCPU int64, currentMemory int64) (predictedCPU, predictedMemory int64) {

	klog.Infof("Predicting for %d CPU and %d memory", currentCPU, currentMemory)
	if currentCPU >= 36000 || currentMemory > 140000 {
		return currentCPU / 2, currentMemory / 2
	}
	return 2 * currentCPU, 2 * currentMemory
}
