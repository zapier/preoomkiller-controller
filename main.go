package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	policy "k8s.io/api/policy/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	_ "k8s.io/klog"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

const (
	EvictionKind                             = "Eviction"
	PreoomkillerPodLabelSelector             = "preoomkiller-enabled=true"
	PreoomkillerAnnotationMemoryThresholdKey = "preoomkiller.alpha.k8s.zapier.com/memory-threshold"
)

// Controller is responsible for ensuring that pods matching PreoomkillerPodLabelSelector
// are evicted.
type Controller struct {
	clientset        kubernetes.Interface
	metricsClientset *metricsv.Clientset
	interval         time.Duration
}

func NewController(clientset kubernetes.Interface, metricsClientset *metricsv.Clientset, interval time.Duration) *Controller {
	return &Controller{
		clientset:        clientset,
		metricsClientset: metricsClientset,
		interval:         interval,
	}
}

// evictPod attempts to evict a pod in a given namespace
func evictPod(client kubernetes.Interface, podName, podNamespace, policyGroupVersion string, dryRun bool) (bool, error) {
	if dryRun {
		return true, nil
	}
	deleteOptions := &meta_v1.DeleteOptions{}
	// GracePeriodSeconds ?
	eviction := &policy.Eviction{
		TypeMeta: meta_v1.TypeMeta{
			APIVersion: policyGroupVersion,
			Kind:       EvictionKind,
		},
		ObjectMeta: meta_v1.ObjectMeta{
			Name:      podName,
			Namespace: podNamespace,
		},
		DeleteOptions: deleteOptions,
	}
	err := client.PolicyV1beta1().Evictions(eviction.Namespace).Evict(eviction)

	if err == nil {
		return true, nil
	} else if apierrors.IsTooManyRequests(err) {
		return false, fmt.Errorf("error when evicting pod (ignoring) %q: %v", podName, err)
	} else if apierrors.IsNotFound(err) {
		return true, fmt.Errorf("pod not found when evicting %q: %v", podName, err)
	} else {
		return false, err
	}
}

// RunOnce runs one sigle iteration of reconciliation loop
func (c *Controller) RunOnce() error {
	evictionCount := 0

	podList, err := c.clientset.CoreV1().Pods("").List(meta_v1.ListOptions{
		LabelSelector: PreoomkillerPodLabelSelector,
	})
	if err != nil {
		log.Errorf("PodListError for label selector %s: %s", PreoomkillerPodLabelSelector, err)
		return err
	}

	for _, pod := range podList.Items {
		podName, podNamespace := pod.ObjectMeta.Name, pod.ObjectMeta.Namespace
		podMemoryThreshold, err := resource.ParseQuantity(pod.ObjectMeta.Annotations[PreoomkillerAnnotationMemoryThresholdKey])
		if err != nil {
			log.WithFields(log.Fields{
				"pod":       podName,
				"namespace": podNamespace,
			}).Errorf("PodMemoryThresholdFetchError: %s", err)
			continue
		}

		podLog := log.WithFields(log.Fields{
			"pod":             podName,
			"namespace":       podNamespace,
			"memoryThreshold": podMemoryThreshold.String(),
		})

		podMemoryUsage := &resource.Quantity{}

		podMetrics, err := c.metricsClientset.MetricsV1beta1().PodMetricses(podNamespace).Get(podName, meta_v1.GetOptions{})
		if err != nil {
			podLog.Errorf("PodMetricsFetchError: %s", err)
			return err
		}

		for _, containerMetrics := range podMetrics.Containers {
			podMemoryUsage.Add(*containerMetrics.Usage.Memory())
			podLog.Debugf("Container metrics for %s: %s (cpu), %s (mem)", containerMetrics.Name, containerMetrics.Usage.Cpu().String(), containerMetrics.Usage.Memory().String())
		}
		podLog.Debugf("Pod memory usage: %v", podMemoryUsage.String())
		if podMemoryUsage.Cmp(podMemoryThreshold) == 1 {
			_, err := evictPod(c.clientset, podName, podNamespace, "v1", false)
			if err != nil {
				podLog.Errorf("PodEvictionError: %v", err)
			} else {
				evictionCount += 1
				podLog.Infof("PodEvicted with memory usage: %v", podMemoryUsage)
			}
		}
	}
	log.Infof("%d pods evicted during this run", evictionCount)
	return nil
}

// Run runs RunOnce in a loop with a delay until stopCh receives a value.
func (c *Controller) Run(stopCh chan struct{}) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		err := c.RunOnce()
		if err != nil {
			log.Error(err)
		}
		select {
		case <-ticker.C:
		case <-stopCh:
			log.Info("Terminating main controller loop")
			return
		}
	}
}

func main() {
	var kubeconfig string
	var master string
	var loglevel string
	var logformat string
	var interval int

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&master, "master", "", "master url")
	flag.IntVar(&interval, "interval", 60, "Interval (in seconds)")
	flag.StringVar(&loglevel, "loglevel", "info", "Log level, one of debug, info, warn, error")
	flag.StringVar(&logformat, "logformat", "text", "Log format, one of json, text")
	flag.Set("logtostderr", "true")
	flag.Parse()

	// Setup logging
	log.SetOutput(os.Stdout)

	switch logformat {
	case "json":
		log.SetFormatter(&log.JSONFormatter{})
	case "text":
	default:
		log.SetFormatter(&log.TextFormatter{})

	}

	switch loglevel {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	case "info":
	default:
		log.SetLevel(log.InfoLevel)
	}

	// creates the connection
	config, err := clientcmd.BuildConfigFromFlags(master, kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	//
	metricsClientset, err := metricsv.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	controller := NewController(clientset, metricsClientset, time.Duration(interval)*time.Second)

	// Now let's start the controller
	stopCh := make(chan struct{})
	go handleSigterm(stopCh)
	defer close(stopCh)
	controller.Run(stopCh)
}

func handleSigterm(stopCh chan struct{}) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	<-signals
	log.Info("Received SIGTERM. Terminating...")
	close(stopCh)
}
