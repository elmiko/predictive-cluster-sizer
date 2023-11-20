# Predictive Cluster Sizer

An experiment about predicting cluster size using historical data.

To build, use go 1.20+, then type `go build` in the root of project.

## Demo:

1. Apply the `predictive_node_scaling_demo.yaml` manifest
2. It will generate a sample data series in the init container and dump it into the the `/data` volume
    - The generated data currently sine-wave oscillates over an hour so it's fast enough that you can see it predict scale your machineset while you're watching it
3. Then the `predictive-cluster-sizer` "controller" will feed the generated data into the prediction server in `autoscaler-predictor-model` to fit the model to the data
4. Then the `predictive-cluster-sizer` "controller" will start asking the `autoscaler-predictor-model` for predictions
5. The `predictive-cluster-sizer` will use those predictions to scale the machineset in your cluster (for now, the first machineset it sees) 

```
[jkyros@jkyros-thinkpadp1gen5 predictive-cluster-sizer]$ oc logs -f shiftweek-cluster-sizer predictive-cluster-sizer
1119 08:42:27.090035       1 main.go:110] "Checking for total nodes in cluster" nodes=15
I1119 08:42:27.090078       1 main.go:129] "Filtering out compute nodes from total" nodes=12
I1119 08:42:27.090086       1 main.go:131] "CPU capacity for compute nodes" value="48"
I1119 08:42:27.090094       1 main.go:136] "Memory resource capacity for compute nodes" value="193115464Ki"
I1119 08:42:27.092881       1 main.go:171] "Each node we have has these resources" cpu="4" memory="16Gi"
I1119 08:42:27.096029       1 main.go:444] REQUESTING: http://localhost:5001/predict?timestamp=2023-11-19T09%3A02%3A27&type=resource
I1119 08:42:27.096097       1 main.go:182] Prediction: CPU: 39702 MEM: 158808
I1119 08:42:27.096148       1 main.go:185] MEMPRC: 0% CPUPRC: 0%	
I1119 08:42:27.096172       1 main.go:191] CPU Delta is -8298
I1119 08:42:27.096192       1 main.go:192] Memory Delta is -29781
I1119 08:42:27.096204       1 main.go:194] Each new node will have: CPU: 4000 MEM: 16384
I1119 08:42:27.096213       1 main.go:200] CPU thinks it needs -2 more nodes 
I1119 08:42:27.096221       1 main.go:201] Memory thinks it needs -1 more nodes
I1119 08:42:27.096228       1 main.go:213] I want to scale machineset jkyros-weekend-keda-s-8mjsf-worker-us-east-1f to 11 desired nodes (currently 13)
I1119 08:42:27.119017       1 main.go:235] Collecting usage ip-10-0-1-62.ec2.internal 4185804Ki 215m
I1119 08:42:27.119036       1 main.go:235] Collecting usage ip-10-0-12-244.ec2.internal 1523636Ki 41m
I1119 08:42:27.119052       1 main.go:235] Collecting usage ip-10-0-18-246.ec2.internal 1633220Ki 48m
I1119 08:42:27.119062       1 main.go:235] Collecting usage ip-10-0-21-208.ec2.internal 1481604Ki 46m
I1119 08:42:27.119073       1 main.go:235] Collecting usage ip-10-0-25-0.ec2.internal 5695232Ki 295m
I1119 08:42:27.119083       1 main.go:235] Collecting usage ip-10-0-30-199.ec2.internal 1547552Ki 39m
I1119 08:42:27.119095       1 main.go:235] Collecting usage ip-10-0-33-17.ec2.internal 1517460Ki 40m
I1119 08:42:27.119110       1 main.go:235] Collecting usage ip-10-0-38-39.ec2.internal 1436012Ki 40m
I1119 08:42:27.119124       1 main.go:235] Collecting usage ip-10-0-46-87.ec2.internal 1662680Ki 55m
I1119 08:42:27.119183       1 main.go:235] Collecting usage ip-10-0-48-177.ec2.internal 1510116Ki 40m
I1119 08:42:27.119200       1 main.go:235] Collecting usage ip-10-0-5-242.ec2.internal 1515616Ki 51m
I1119 08:42:27.119214       1 main.go:235] Collecting usage ip-10-0-51-174.ec2.internal 1583148Ki 45m
I1119 08:42:27.119225       1 main.go:251] Total usage CPU: 955 MEM: 24699
I1119 08:42:27.119239       1 main.go:281] Scaling machineset jkyros-weekend-keda-s-8mjsf-worker-us-east-1f to 11 desired nodes (currently 13 ready 12)

```
