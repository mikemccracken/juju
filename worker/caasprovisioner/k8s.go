// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasprovisioner

import (
	"github.com/juju/errors"
	"github.com/juju/loggo"
	"gopkg.in/juju/names.v2"
	"k8s.io/client-go/kubernetes"
	k8serrors "k8s.io/client-go/pkg/api/errors"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"

	apicaasprovisioner "github.com/juju/juju/api/caasprovisioner"
)

var logger = loggo.GetLogger("juju.worker.caasprovisioner")

// TODO(caas) should be using a juju specific namespace
const namespace = "default"

func newK8sClient(st *apicaasprovisioner.State) (*kubernetes.Clientset, error) {
	config, err := newK8sConfig(st)
	if err != nil {
		return nil, errors.Trace(err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return client, nil
}

func newK8sConfig(st *apicaasprovisioner.State) (*rest.Config, error) {
	config, err := st.ProvisioningConfig()
	if err != nil {
		return nil, errors.Trace(err)
	}

	CAData := make([]byte, 1024)
	for _, cacert := range config.CACertificates {
		CAData = append(CAData, cacert...)
	}

	return &rest.Config{
		Host:     config.Endpoint,
		Username: config.Username,
		Password: config.Password,
		TLSClientConfig: rest.TLSClientConfig{
			CertData: config.CertData,
			KeyData:  config.KeyData,
			CAData:   CAData,
		},
	}, nil
}

type newConfigFunc func(appName string) ([]byte, error)

func ensureOperator(client *kubernetes.Clientset, appName string, newConfig newConfigFunc) error {
	if exists, err := operatorExists(client, appName); err != nil {
		return errors.Trace(err)
	} else if exists {
		logger.Infof("%s operator already deployed", appName)
		return nil
	}
	logger.Infof("deploying %s operator", appName)

	configMapName, err := ensureConfigMap(client, appName, newConfig)
	if err != nil {
		return errors.Trace(err)
	}

	return deployOperator(client, appName, configMapName)
}

func ensureConfigMap(client *kubernetes.Clientset, appName string, newConfig newConfigFunc) (string, error) {
	mapName := podName(appName) + "-config"

	exists, err := configMapExists(client, mapName)
	if err != nil {
		return "", errors.Trace(err)
	}
	if exists {
		logger.Infof("ConfigMap %s already exists", mapName)
	} else {
		config, err := newConfig(appName)
		if err != nil {
			return "", errors.Annotate(err, "creating config")
		}
		if err := createConfigMap(client, mapName, config); err != nil {
			return "", errors.Annotate(err, "creating ConfigMap")
		}
	}
	return mapName, nil
}

func configMapExists(client *kubernetes.Clientset, configMapName string) (bool, error) {
	_, err := client.CoreV1().ConfigMaps(namespace).Get(configMapName)
	if k8serrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, errors.Trace(err)
	}
	return true, nil
}

func createConfigMap(client *kubernetes.Clientset, configMapName string, config []byte) error {
	_, err := client.ConfigMaps(namespace).Create(&v1.ConfigMap{
		ObjectMeta: v1.ObjectMeta{
			Name: configMapName,
		},
		Data: map[string]string{
			"agent.conf": string(config),
		},
	})
	return errors.Trace(err)
}

func operatorExists(client *kubernetes.Clientset, appName string) (bool, error) {
	_, err := client.CoreV1().Pods(namespace).Get(podName(appName))
	if k8serrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, errors.Trace(err)
	}
	return true, nil
}

func deployOperator(client *kubernetes.Clientset, appName string, configMapName string) error {
	configVolName := configMapName + "-volume"

	appTag := names.NewApplicationTag(appName)
	spec := &v1.Pod{
		ObjectMeta: v1.ObjectMeta{
			Name: podName(appName),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{
				Name:            "juju-operator",
				Image:           "mikemccracken/caasoperator", // TODO(caas) use canonical image name
				ImagePullPolicy: v1.PullIfNotPresent,
				Args:            []string{"caasoperator", "--application-name", appName, "--debug"},
				VolumeMounts: []v1.VolumeMount{{
					Name: configVolName,
					// XXX shouldn't be hardcoded
					MountPath: "/var/lib/juju/agents/" + appTag.String() + "/agent.conf",
					SubPath:   "agent.conf",
				}},
			}},
			Volumes: []v1.Volume{{
				Name: configVolName,
				VolumeSource: v1.VolumeSource{
					ConfigMap: &v1.ConfigMapVolumeSource{
						LocalObjectReference: v1.LocalObjectReference{
							Name: configMapName,
						},
						Items: []v1.KeyToPath{{
							Key:  "agent.conf",
							Path: "agent.conf",
						}},
					},
				},
			}},
		},
	}
	_, err := client.CoreV1().Pods(namespace).Create(spec)
	return errors.Trace(err)
}

func podName(appName string) string {
	return "juju-operator-" + appName
}
