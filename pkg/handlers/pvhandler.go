package handlers

import (
	"os"
	"log"
	"time"
	"reflect"
	"errors"
	"strings"
	syscall "golang.org/x/sys/unix"
	"github.com/nokia/dynamic-local-pv-provisioner/pkg/k8sclient"

	"k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/rest"
	"k8s.io/apimachinery/pkg/api/resource"
)

type PvHandler struct {
	nodeName    string
	storagePath string
	k8sClient   kubernetes.Interface
}

func NewPvHandler(storagePath string, cfg *rest.Config) (*PvHandler, error) {
	log.Println("DEBUG: NewPvHandler start...")
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	nodeName := os.Getenv("NODE_NAME")
	pvHandler := PvHandler{
		nodeName:    nodeName,
		storagePath: storagePath,
		k8sClient:   kubeClient,
	}
	log.Println("DEBUG: pvHandler setted")
	lvCap, err := lvmAvailableCapacity(storagePath)
	if err != nil{
		return nil, err
	}
	log.Printf("DEBUG: lvmCap determined, lvcap: %d", lvCap)
	err = createLVCapacityResource(nodeName, lvCap, kubeClient)
	log.Println("DEBUG: after patch")
	return &pvHandler, err
}

func (pvHandler *PvHandler) CreateController() cache.Controller {
	kubeInformerFactory := informers.NewSharedInformerFactory(pvHandler.k8sClient, time.Second*30)
	controller := kubeInformerFactory.Core().V1().PersistentVolumes().Informer()
	controller.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { pvHandler.pvAdded(*(reflect.ValueOf(obj).Interface().(*v1.PersistentVolume))) },
		DeleteFunc: func(obj interface{}) { pvHandler.pvDeleted(*(reflect.ValueOf(obj).Interface().(*v1.PersistentVolume))) },
		UpdateFunc: func(oldObj, newObj interface{}) {},
	})
	return controller
}

func (pvHandler *PvHandler) pvAdded(pv v1.PersistentVolume) {
	log.Printf("PV Added: %+v\n", pv)
	if !pvHandler.handlePv(pv) {
		log.Println("DEBUG: PV Added - Not my job...")
		return
	}
	err := pvHandler.decreaseStorageCap(pv)
	if err != nil{
		log.Println("ERROR: PV Added failed: " + err.Error())
		return
	}
	log.Println("DEBUG: PV Added successfull ")
}

func (pvHandler *PvHandler) pvDeleted(pv v1.PersistentVolume) {
	log.Printf("DEBUG: PV Deleted: %+v\n", pv)
	if !pvHandler.handlePv(pv) {
		log.Println("DEBUG: PV Deleted - Not my job...")
		return
	}
	err := pvHandler.increaseStorageCap(pv)
	if err != nil{
		log.Println("ERROR: PV Delete failed: " + err.Error())
		return
	}
	log.Println("DEBUG: PV Delete successfull")
}

func (pvHandler *PvHandler) handlePv(pv v1.PersistentVolume) bool {
	nodeSelector := pv.Spec.NodeAffinity.Required.String()
	if strings.Contains(nodeSelector, pvHandler.nodeName) {
		log.Println("DEBUG: handle PV")
		return true
	}
	log.Println("DEBUG: nodeselector: " + nodeSelector)
	return false
}

func (pvHandler *PvHandler) increaseStorageCap(pv v1.PersistentVolume) error{
	pvCapacity := pv.Spec.Capacity["storage"]
	node, err := k8sclient.GetNode(pvHandler.nodeName, pvHandler.k8sClient)
	if err != nil{
		return errors.New("Cannot get node(" + pvHandler.nodeName + "), because: " + err.Error())
	}
	nodeCap := node.Status.Capacity[k8sclient.LvCapacity]
	(&nodeCap).Add(pvCapacity)
	node.Status.Capacity[k8sclient.LvCapacity] = nodeCap
	err = k8sclient.UpdateNodeStatus(pvHandler.nodeName, pvHandler.k8sClient, node)
	if err != nil{
		return errors.New("Cannot update node(" + pvHandler.nodeName + "), because: " + err.Error())
	}
	return nil
}

func (pvHandler *PvHandler) decreaseStorageCap(pv v1.PersistentVolume) error{
	pvCapacity := pv.Spec.Capacity["storage"]
	node, err := k8sclient.GetNode(pvHandler.nodeName, pvHandler.k8sClient)
	if err != nil{
		return errors.New("Cannot get node(" + pvHandler.nodeName + "), because: " + err.Error())
	}
	log.Printf("DEBUG: dec pv-capacity: %+v\n", pvCapacity)
	nodeCap := node.Status.Capacity[k8sclient.LvCapacity]
	(&nodeCap).Sub(pvCapacity)
	node.Status.Capacity[k8sclient.LvCapacity] = nodeCap
	err = k8sclient.UpdateNodeStatus(pvHandler.nodeName, pvHandler.k8sClient, node)
	if err != nil{
		return errors.New("Cannot update node(" + pvHandler.nodeName + "), because: " + err.Error())
	}
	return nil
}

func createLVCapacityResource(nodeName string, lvCapacity int64, kubeClient kubernetes.Interface) error {
	node, err := k8sclient.GetNode(nodeName, kubeClient)
	if err != nil{
		return errors.New("Cannot get node(" + nodeName + "), because: " + err.Error())
	}
	lvCapQuantity := resource.NewQuantity(lvCapacity, resource.BinarySI)
	node.Status.Capacity[k8sclient.LvCapacity] = *lvCapQuantity
	err = k8sclient.UpdateNodeStatus(nodeName, kubeClient, node)
	if err != nil{
		return errors.New("Cannot update node(" + nodeName + "), because: " + err.Error())
	}
	return nil
}

func lvmAvailableCapacity (lvPath string) (int64, error) {
	fs := syscall.Statfs_t{}
	err := syscall.Statfs(lvPath, &fs)
	if err != nil {
		return 0, errors.New("Cannot get FS info from: " + lvPath + " because: " + err.Error())
	}
	log.Printf("DEBUG: Availabe blocks: %d , int64: %d , Block size: %d",fs.Bavail, int64(fs.Bavail), fs.Bsize)
	return int64(fs.Bavail) * fs.Bsize, nil
}
