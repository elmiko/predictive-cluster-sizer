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
	"context"
	"flag"
	"fmt"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
)

const (
	MASTER_NODE_LABEL = "node-role.kubernetes.io/master"
)

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}
	fmt.Printf("Found %d nodes in the cluster\n", len(nodes.Items))
	computeNodes := []corev1.Node{}
	for _, node := range nodes.Items {
		if _, found := node.Labels[MASTER_NODE_LABEL]; !found {
			computeNodes = append(computeNodes, node)
		}
	}
	result := corev1.ResourceList{}
	for _, node := range computeNodes {
		for resource, value := range node.Status.Capacity {
			if current, ok := result[resource]; ok {
				result[resource] = sumQuantity(current, value)
			} else {
				result[resource] = value
			}
		}
	}
	fmt.Printf("%d compute nodes\n", len(computeNodes))
	fmt.Printf("Compute node resources:\n")
	for resource, value := range result {
		fmt.Printf("- %s: %s\n", resource, value.String())
	}
}

func sumQuantity(left, right resource.Quantity) resource.Quantity {
	result := resource.Quantity{}
	result.Add(left)
	result.Add(right)
	return result
}

func print_machines(config *restclient.Config) {
	clientset, err := machinev1beta1.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	namespace := "openshift-machine-api"
	machines, err := clientset.Machines(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}
	fmt.Printf("There are %d machines in the cluster\n", len(machines.Items))

	for _, machine := range machines.Items {
		fmt.Printf("  %s\n", machine.Name)
	}
}
