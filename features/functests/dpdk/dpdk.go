package dpdk

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift-kni/baremetal-deploy/features/functests/utils/namespace"
	"github.com/openshift-kni/baremetal-deploy/features/functests/utils/clients"
)

const (
	dpdkHostLabel         = "feature.node.kubernetes.io/network-sriov.capable=true"
	hostnameLabel         = "kubernetes.io/hostname"
	dpdkAnnotationNetwork = "dpdk-network"
	testDpdkNamespace     = "dpdk-testing"
	testCmdPath           = "/opt/test.sh"
)

var dpdkAppImage string
var c *k8sv1.ConfigMap

func init() {
	// Set DPDK app image
	dpdkAppImage = os.Getenv("DPDK_APP_IMAGE")
	if dpdkAppImage == "" {
		dpdkAppImage = "docker.io/dorzheh/dpdk-centos7:latest"
		//"quay.io/schseba/dpdk-prod:test"
	}
}

var _ = Describe("dpdk", func() {
	var _ = Context("Run a DPDK app on each worker", func() {
		beforeAll(func() {
			namespace.Create(testDpdkNamespace, clients.K8s)
		})

		It("Should forward and receive packets", func() {
			nodes := getListOfNodes(dpdkHostLabel)
			c = createTestpmdConfigMap(testDpdkNamespace)
			for _, n := range nodes {
				p := createTestPod(n.Name, testDpdkNamespace, c.Name)
				waitForReadiness(p.Namespace, p.Name)
				By(fmt.Sprintf("Execute %s inside the pod %s", testCmdPath, p.Name))
				out, err := exec.Command("oc", "rsh", "-n", p.Namespace, p.Name, "bash", "-c", testCmdPath).CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("cannot execute %s inside the pod %s", testCmdPath, p.Name) )
				checkRxTx(string(out))
				deleteTestPod(p.Name)
			}
			deleteTestpmdConfigMap(c.Name)
		})
	})
})

// creteTestPod creates a pod that will act as a runtime for the DPDK test application
func createTestPod(nodeName, namespace, configMapName string) *k8sv1.Pod {
	defaultMode := int32(0755)

	res := &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-dpdk",
			Labels: map[string]string{
				"app": "test-dpdk",
			},
			Annotations: map[string]string{
				"k8s.v1.cni.cncf.io/networks": dpdkAnnotationNetwork,
			},
			Namespace: namespace,
		},
		Spec: k8sv1.PodSpec{
			RestartPolicy: k8sv1.RestartPolicyNever,
			Containers: []k8sv1.Container{
				{
					Name:  "test-dpdk",
					Image: dpdkAppImage,
					SecurityContext: &k8sv1.SecurityContext{
						Capabilities: &k8sv1.Capabilities{
							Add: []k8sv1.Capability{"IPC_LOCK"},
						},
					},
					Command:         []string{"/bin/bash", "-c", "--"},
					Args:            []string{"while true; do sleep inf; done;"},
					ImagePullPolicy: "Always",
					Resources: k8sv1.ResourceRequirements{
						Limits: k8sv1.ResourceList{
							k8sv1.ResourceCPU:                     resource.MustParse("4"),
							k8sv1.ResourceMemory:                  resource.MustParse("1000Mi"),
							k8sv1.ResourceHugePagesPrefix + "1Gi": resource.MustParse("4Gi"),
						},
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceCPU:                     resource.MustParse("4"),
							k8sv1.ResourceMemory:                  resource.MustParse("1000Mi"),
							k8sv1.ResourceHugePagesPrefix + "1Gi": resource.MustParse("4Gi"),
						},
					},
					VolumeMounts: []k8sv1.VolumeMount{
						{
							Name:      "hugepage",
							MountPath: "/mnt/huge",
							ReadOnly:  false,
						},
						{
							Name:      "testcmd",
							MountPath: testCmdPath,
							SubPath:   "test.sh",
						},
					},
				},
			},
			Volumes: []k8sv1.Volume{
				{
					Name: "hugepage",
					VolumeSource: k8sv1.VolumeSource{
						EmptyDir: &k8sv1.EmptyDirVolumeSource{
							Medium: k8sv1.StorageMediumHugePages,
						},
					},
				},
				{
					Name: "testcmd",
					VolumeSource: k8sv1.VolumeSource{
						ConfigMap: &k8sv1.ConfigMapVolumeSource{
							k8sv1.LocalObjectReference{Name: configMapName},
							nil,
							&defaultMode,
							nil,
						},
					},
				},
			},

			NodeSelector: map[string]string{
				hostnameLabel: nodeName,
			},
		},
	}

	By("Create a test pod")
	p, err := clients.K8s.CoreV1().Pods(namespace).Create(res)
	Expect(err).ToNot(HaveOccurred(), "cannot create the test pod")
	return p
}

// getListOfNodes finds appropriate nodes
func getListOfNodes(nodeLabel string) []k8sv1.Node {
	By("Getting list of nodes")
	nodes, err := clients.K8s.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: nodeLabel,
	})
	Expect(err).ToNot(HaveOccurred())
	Expect(len(nodes.Items)).Should(BeNumerically(">", 0), "cannot find nodes labeled as "+nodeLabel)
	return nodes.Items
}


// createTestpmdConfigMap creates a ConfigMap that mounts testpmd wrapper script
func createTestpmdConfigMap(namespace string) *k8sv1.ConfigMap {
	m := &k8sv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "testpmd",
			Namespace: namespace,
		},
		Data: map[string]string{
			"test.sh": `#!/usr/bin/env bash
                        export CPU=$(cat /sys/fs/cgroup/cpuset/cpuset.cpus)
                        echo ${CPU}
                        echo ${PCIDEVICE_OPENSHIFT_IO_DPDKNIC}
                        testpmd -l ${CPU} -w ${PCIDEVICE_OPENSHIFT_IO_DPDKNIC}  -- -a --portmask=0x1 --nb-cores=2 --forward-mode=mac
                       `,
		},
	}

	By("Create testpmd wrapper script")
	m, err := clients.K8s.CoreV1().ConfigMaps(testDpdkNamespace).Create(createTestpmdConfigMap(testDpdkNamespace))
	Expect(err).ToNot(HaveOccurred(), "cannot create testpmd wrapper script")
	return m
}

func deleteTestpmdConfigMap(configMapName string) {
	err := clients.K8s.CoreV1().ConfigMaps(testDpdkNamespace).Delete(configMapName, &metav1.DeleteOptions{})
	Expect(err).ToNot(HaveOccurred())
}

func deleteTestPod(podName string) {
	err := clients.K8s.CoreV1().Pods(testDpdkNamespace).Delete(podName, &metav1.DeleteOptions{})
	Expect(err).ToNot(HaveOccurred())
}

// witForReadiness blocks the flow until the pod phase will be "Running"
func waitForReadiness(namespace, podName string) {
	Eventually(func() k8sv1.PodPhase {
		pod, err := clients.K8s.CoreV1().Pods(namespace).Get(podName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		return pod.Status.Phase
	}, 2*time.Minute, 1*time.Second).Should(Equal(k8sv1.PodRunning))

}

// checkRxTx parses the output from the DPDK test application
// and verifies that packets have passed the NIC TX and RX queues
func checkRxTx(out string) {
	str := strings.Split(out, "\n")
	for i := 0; i < len(str); i++ {
		if strings.Contains(str[i], "all ports") {
			i++
			r := strings.Fields(str[i])
			Expect(len(r)).To(Equal(6), "the slice doesn't contain 6 elements")
			d, err := strconv.Atoi(r[5])
			Expect(err).ToNot(HaveOccurred())
			Expect(d).Should(BeNumerically(">", 0), "number of received packets should be greater then 0")

			i++
			r = strings.Fields(str[i])
			Expect(len(r)).To(Equal(6), "the slice doesn't contain 6 elements")
			d, err = strconv.Atoi(r[5])
			Expect(err).ToNot(HaveOccurred())
			Expect(d).Should(BeNumerically(">", 0), "number of transferred packets should be greater then 0")

		}
	}
}

func beforeAll(fn func()) {
	first := true
	BeforeEach(func() {
		if first {
			first = false
			fn()
		}
	})
}
