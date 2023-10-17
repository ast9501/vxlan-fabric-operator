/*
Copyright 2023.

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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"strings"
	"net"
	"bytes"

	topov1alpha1 "github.com/ast9501/vxlan-fabric-operator/api/v1alpha1"
	internal "github.com/ast9501/vxlan-fabric-operator/internal"
)

var logger = log.Log.WithName("controller_vxlan")

const finalizerName string = "vxlanfabric.finalizer.win.nycu"

// TN Manager api endpoints prefix
const vxlanCommApi string = "/api/v1/vxlan/"
const bridgeCommApi string = "/api/v1/bridge/"
const sliceCommApi string = "/api/v1/slice/"
var vxlanIntfIpStart string = "192.168.3.220"


type createVxlanBrPayload struct {
	BindInterface  string `json:"bindInterface"`
	LocalBrIp      string `json:"localBrIp"`
	RemoteIp       string `json:"remoteIp"`
	VxlanId        string `json:"vxlanId"`
	VxlanInterface string `json:"vxlanInterface"`
}

type createSlicePayload struct {
	DstIp 		string 	`json:"DstIP"`
	FlowRate 	int 	`json:FlowRate`
	SliceSd 	string 	`json:SliceSD`
	SrcIp		string 	`json:"SrcIP"`
}

const (
	StateNull       string = ""
	StateActivating string = "Activating"
	StateActivated  string = "Activated"
)

// VxlanFabricReconciler reconciles a VxlanFabric object
type VxlanFabricReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=topo.winlab.nycu,resources=vxlanfabrics,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=topo.winlab.nycu,resources=vxlanfabrics/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=topo.winlab.nycu,resources=vxlanfabrics/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VxlanFabric object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *VxlanFabricReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// TODO(user): your logic here
	logger.Info("Reconcile vxlan fabric", "Request.Namespace", req.Namespace, "Request.Name", req.Name)
	instance := &topov1alpha1.VxlanFabric{}
	err := r.Client.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found
			logger.Error(err, "Request object not found")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Check if object is under deletion
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted
		if !containsString(instance.ObjectMeta.Finalizers, finalizerName) {
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, finalizerName)
			if err := r.Client.Update(context.Background(), instance); err != nil {
				return reconcile.Result{}, err
			}
		}
	} else {
		// The object is being deleted
		if containsString(instance.ObjectMeta.Finalizers, finalizerName) {
			// The finalizer is present
			// Delete the fabric
			for _, e := range instance.Spec.NodeList {
				//TODO: Only remove single slice while there are still another slice on vxlan tunnel
				//FIXME: Check if vxlan bridge exist before delete it
				api := newDelVxlanReq(e.Endpoint, e.Iface)
				client := &http.Client{}
				req, err := http.NewRequest("DELETE", api, nil)
				if err != nil {
					logger.Error(err, "Failed to build delete vxlan request")
				}

				resp, err := client.Do(req)
				if err != nil {
					logger.Error(err, "Failed to send delete vxlan request", "Request.Url", api)
				} else {
					if resp.StatusCode <= 300 {
						logger.Info("Remove the vxlan interface", "Request.Url", api)
					} else {
						logger.Info("Remove the vxlan interface in unexpected response status", "Request.Url", api, "StatusCode", resp.StatusCode)
					}
				}
				resp.Body.Close()
				//defer resp.Body.Close()
			}

			// Remove finalizer
			instance.ObjectMeta.Finalizers = removeFinalizer(instance.ObjectMeta.Finalizers, finalizerName)
			err = r.Update(ctx, instance)
			if err != nil {
				return reconcile.Result{}, err
			} else {
				logger.Info("Finalizer removed")
			}
		}

		// Stop reconcliiation as the object is being deleted
		logger.Info("Stop reconciling")
		return reconcile.Result{}, nil
	}

	// Activate two nodes vxlan interface
	if instance.Status.State == StateActivating || instance.Status.State == StateActivated {
		logger.Info("Topology is activate")
		return reconcile.Result{}, nil
	} else if instance.Status.State != StateNull {
		err := fmt.Errorf("unknown Topology state %s", instance.Status.State)
		return reconcile.Result{}, err
	}

	// Update Object state to Activating
	instance.Status.State = StateActivating
	logger.Info("New Reconcile", "instance name", instance.Name)
	err = r.Client.Status().Update(context.TODO(), instance)
	if err != nil {
		logger.Error(err, "Failed to update VXLAN fabric topology state")
		return reconcile.Result{}, err
	}

	var activateState map[string]int = make(map[string]int)

	s := internal.NewStack()
	// Parse all nodes ip
	for _, e := range instance.Spec.NodeList {
		endpointParts := strings.Split(e.Endpoint, ":")
		s.Push(&internal.Node{endpointParts[0]})
	}

	// Keep central node api endpoints (first object of NodeList as central node)
	var centralNodeBaseUrl string = ""
	var centralNodeIntf string = ""

	for i, e := range instance.Spec.NodeList {
		// Activate the vxlan interface in topology
		logger.Info("Activate vxlan interface on node", "node index", i, "node endpoint", e.Endpoint, "node interface", e.Iface)

		if i == 0 {
			instance.Status.Node1Ip = e.Endpoint
			centralNodeBaseUrl = e.Endpoint
			centralNodeIntf = e.Iface
		} else if i == 1 {
			instance.Status.Node2Ip = e.Endpoint
		}

		// If vxlan bridge exist, skip
		getVxlanApi := newGetVxlanReq(e.Endpoint, e.Iface)
		client := &http.Client{}
		req, err := http.NewRequest("GET", getVxlanApi, nil)
		if err != nil {
			logger.Error(err, "Failed to build get vxlan request")
		}

		resp, err := client.Do(req)

		if err != nil {
			logger.Error(err, "Failed to retrieve vxlan status", "Request.Url", getVxlanApi)
		} else {
			if resp.StatusCode < 400 {
				logger.Info("Vxlan interface existed")
				continue
			} else {
				logger.Info("Vxlan interface not existed, request for new vxlan interface", "Node", e.Endpoint)
			}
		}

		// If vxlan bridge not exist, create new vxlan bridge
		newVxlanApi := newCreateVxlanReq(e.Endpoint, e.Iface)
		
		// Build create vxlan body
		newVxlanBridgeIpv4 := newIp()
		newVxlanPayload, err := newCreateVxlanBody(e.OutgoingFace, newVxlanBridgeIpv4, s.Pop().Value, "100", "vxlan100")
		if err != nil {
			logger.Error(err, "Failed to build create vxlan request body")
		}
		req, err = http.NewRequest("POST", newVxlanApi, bytes.NewReader([]byte(newVxlanPayload)))
		if err != nil {
			logger.Error(err, "Failed to build create vxlan request")
		}

		resp, err = client.Do(req)
		if err != nil {
			logger.Error(err, "Failed to send create vxlan request", "Request.Url", newVxlanApi)
		} else {
			if resp.StatusCode < 300 {
				logger.Info("vxlan interface is activated", "Request.Url", newVxlanApi, "Request.Iface", e.Iface)
				activateState[e.Endpoint] = 1
			} else {
				logger.Info("Activate the vxlan interface in unexpected response status, check the TN Manager logs on node ", "Request.Url", newVxlanApi, "StatusCode", resp.StatusCode)
				activateState[e.Endpoint] = 0
			}
		}
		resp.Body.Close()
	}

	// Check if vxlan bridge activated
	for k, v := range activateState {
		if v != 1 {
			logger.Info("The vxlan interface on Node is not activated", "Node.Endpoint", k)
			return ctrl.Result{}, nil
		}
	}
	logger.Info("All nodes in topology are activated")

	// Create new slice on vxlan bridge
	logger.Info("Start to install slice rules")

	// Apply slice rules on central node only
	createSliceApi := newCreateSliceReq(centralNodeBaseUrl, centralNodeIntf)

	// Loop: create slice
	for _, e := range instance.Spec.Slice {

		client := &http.Client{}
		// Build slice create body
		payload, err := newCreateSliceBody(e.Dst, e.Rate, e.Sd, e.Src)
		if err != nil {
			logger.Error(err, "Failed to build create slice body")
		}
		
		req, err := http.NewRequest("POST", createSliceApi, bytes.NewReader([]byte(payload)))
		if err != nil {
			logger.Error(err, "Failed to build create slice request")
		}

		resp, err := client.Do(req)

		if err != nil {
			logger.Error(err, "Failed to launch create slice request", "Request.Url", createSliceApi)
		} else {
			if resp.StatusCode < 400 {
				logger.Info("Slice installation successfully")
				continue
			} else {
				logger.Info("Error occured during slice installation procedure, check tn agent logs for more info", "Node", centralNodeBaseUrl)
			}
		}
	}

	instance.Status.State = StateActivated
	err = r.Client.Status().Update(context.TODO(), instance)

	if err != nil {
		logger.Error(err, "Failed to update VXLAN fabric topology state")
		return reconcile.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VxlanFabricReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&topov1alpha1.VxlanFabric{}).
		Complete(r)
}

// Helper functions to check and remove string from a slice of strings.
// See https://github.com/kubernetes-sigs/kubebuilder/blob/master/docs/book/src/cronjob-tutorial/testdata/finalizer_example.go

// containsString checks if the given slice of string contains the target string
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func newDelVxlanReq(url, intf string) string {
	return fmt.Sprintf("http://"+url+vxlanCommApi+"%s", intf)
}

func newGetVxlanReq(url, intf string) string {
	return fmt.Sprintf("http://"+url+bridgeCommApi+"%s", intf)
}

func newCreateVxlanReq(url, intf string) string {
	return fmt.Sprintf("http://"+url+vxlanCommApi+"%s/", intf)
}

func newCreateSliceReq(url, intf string) string {
	return fmt.Sprintf("http://"+url+sliceCommApi+"%s/", intf)
}

func newCreateVxlanBody(bindIntf, vxlanBrIp, remoteIntfIp, vxlanId, vxlanIntfName string) (string, error) {
	payload := createVxlanBrPayload{
		BindInterface:  bindIntf,
		LocalBrIp:      vxlanBrIp,
		RemoteIp:       remoteIntfIp,
		VxlanId:        vxlanId,
		VxlanInterface: vxlanIntfName,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func newCreateSliceBody(dstIp string, flowRate int, sliceSd string, srcIp string) (string, error) {
	payload := createSlicePayload{
		DstIp: dstIp,
		FlowRate: flowRate,
		SliceSd: sliceSd,
		SrcIp: srcIp,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// Allocate new ip for vxlan bridge interface
func newIp() string {
	ip := net.ParseIP(vxlanIntfIpStart)
	ipv4 := ip.To4()
	// FIXME: Dynamic generate new ip with CIDR (fixed to 24 now)
	ipv4[3] += 1
	nextIpv4 := net.IPv4(ipv4[0], ipv4[1], ipv4[2], ipv4[3])
	vxlanIntfIpStart = nextIpv4.String()
	logger.Info("Generate new vxlan bridge ip", "ipv4", ipv4.String() + "/24")
	return ipv4.String() + "/24"
}

// removeFinalizer removes the given finalizer from the finalizers slice
func removeFinalizer(finalizers []string, finalizer string) []string {
	var updatedFinalizers []string
	for _, f := range finalizers {
		if f != finalizer {
			updatedFinalizers = append(updatedFinalizers, f)
		}
	}
	return updatedFinalizers
}
