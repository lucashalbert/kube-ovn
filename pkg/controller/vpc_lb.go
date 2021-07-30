package controller

import (
	"context"
	"fmt"
	"strings"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

func vpcLbDeploymentName(vpc string) string {
	return fmt.Sprintf("vpc-%s-lb", vpc)
}

func (c *Controller) createVpcLb(vpc *kubeovnv1.Vpc) error {
	deployment, err := c.genVpcLbDeployment(vpc)
	if deployment == nil || err != nil {
		return err
	}

	_, err = c.config.KubeClient.AppsV1().Deployments(c.config.PodNamespace).Get(context.Background(), deployment.Name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		klog.Errorf("failed to check LB deployment for VPC %s: %v", vpc.Name, err)
		return err
	}

	if _, err = c.config.KubeClient.AppsV1().Deployments(c.config.PodNamespace).Create(context.Background(), deployment, metav1.CreateOptions{}); err != nil {
		klog.Errorf("failed to create LB deployment for VPC %s: %v", vpc.Name, err)
		return err
	}

	return nil
}

func (c *Controller) deleteVpcLb(vpc *kubeovnv1.Vpc) error {
	name := vpcLbDeploymentName(vpc.Name)
	_, err := c.config.KubeClient.AppsV1().Deployments(c.config.PodNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}

		klog.Errorf("failed to check LB deployment for VPC %s: %v", vpc.Name, err)
		return err
	}

	if err = c.config.KubeClient.AppsV1().Deployments(c.config.PodNamespace).Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		klog.Errorf("failed to delete LB deployment of PVC %s: %v", vpc.Name, err)
		return err
	}

	return nil
}

func (c *Controller) genVpcLbDeployment(vpc *kubeovnv1.Vpc) (*v1.Deployment, error) {
	if len(vpc.Status.Subnets) == 0 {
		return nil, nil
	}

	subnet, err := c.subnetsLister.Get(vpc.Status.DefaultLogicalSwitch)
	if err != nil {
		return nil, err
	}

	replicas := int32(1)
	name := vpcLbDeploymentName(vpc.Name)
	allowPrivilegeEscalation := true
	privileged := true
	labels := map[string]string{
		"app":           name,
		util.VpcLbLabel: "true",
	}

	podAnnotations := map[string]string{
		util.VpcAnnotation:               vpc.Name,
		util.LogicalSwitchAnnotation:     subnet.Name,
		util.AttachmentNetworkAnnotation: util.VpcLbNetworkAttachment,
	}

	deployment := &v1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				util.VpcAnnotation:   vpc.Name,
				util.VpcLbAnnotation: "true",
			},
		},
		Spec: v1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: podAnnotations,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{},
					Containers: []corev1.Container{
						{
							Name:            "vpc-lb",
							Image:           "kubeovn/vpc-nat-gateway:v1.8.0",
							Command:         []string{"bash"},
							Args:            []string{"-c", "while true; do sleep 10000; done"},
							ImagePullPolicy: corev1.PullIfNotPresent,
							SecurityContext: &corev1.SecurityContext{
								Privileged:               &privileged,
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
							},
						},
					},
				},
			},
			Strategy: v1.DeploymentStrategy{
				Type: v1.RecreateDeploymentStrategyType,
			},
		},
	}

	v4Gw, v6Gw := util.SplitStringIP(subnet.Spec.Gateway)
	v4Svc, v6Svc := util.SplitStringIP(c.config.ServiceClusterIPRange)
	if v4Gw != "" && v4Svc != "" {
		deployment.Spec.Template.Spec.InitContainers = append(deployment.Spec.Template.Spec.InitContainers, corev1.Container{
			Name:            "init-ipv4-route",
			Image:           "kubeovn/vpc-nat-gateway:v1.8.0",
			Command:         []string{"ip"},
			Args:            strings.Fields(fmt.Sprintf("-4 route add %s via %s", v4Svc, v4Gw)),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				Privileged:               &privileged,
				AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			},
		}, corev1.Container{
			Name:            "init-ipv4-iptables",
			Image:           "kubeovn/vpc-nat-gateway:v1.8.0",
			Command:         []string{"iptables"},
			Args:            strings.Fields(fmt.Sprintf("-t nat -I POSTROUTING -d %s -j MASQUERADE", v4Svc)),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				Privileged:               &privileged,
				AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			},
		})
	}
	if v6Gw != "" && v6Svc != "" {
		deployment.Spec.Template.Spec.InitContainers = append(deployment.Spec.Template.Spec.InitContainers, corev1.Container{
			Name:            "init-ipv6-route",
			Image:           "kubeovn/vpc-nat-gateway:v1.8.0",
			Command:         []string{"ip"},
			Args:            strings.Fields(fmt.Sprintf("-6 route add %s via %s", v6Svc, v6Gw)),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				Privileged:               &privileged,
				AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			},
		}, corev1.Container{
			Name:            "init-ipv6-iptables",
			Image:           "kubeovn/vpc-nat-gateway:v1.8.0",
			Command:         []string{"ip6tables"},
			Args:            strings.Fields(fmt.Sprintf("-t nat -I POSTROUTING -d %s -j MASQUERADE", v6Svc)),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				Privileged:               &privileged,
				AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			},
		})
	}

	return deployment, nil
}
