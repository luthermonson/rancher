package clean

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

const ContainerName = "cattle-node-cleanup"

func Job() error {
	logrus.Infof("Starting clean container job: %s", ContainerName)

	c, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}
	defer c.Close()

	ctx := context.Background()

	containerList, err := c.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return err
	}

	for _, c := range containerList {
		for _, n := range c.Names {
			if n == "/"+ContainerName {
				logrus.Infof("container named %s already exists, exiting.", ContainerName)
				return nil
			}
		}
	}

	binds := []string{
		"\\\\.\\pipe\\docker_engine:\\\\.\\pipe\\docker_engine",
		"c:\\:c:\\host:z",
		"\\\\.\\pipe\\rancher_wins:\\\\.\\pipe\\rancher_wins",
	}

	container, err := c.ContainerCreate(ctx, &container.Config{
		Image: getAgentImage(),
		Env: []string{
			"AGENT_IMAGE=" + getAgentImage(),
			"PREFIX_PATH=" + os.Getenv("PREFIX_PATH"),
			"WINDOWS_PREFIX_PATH=" + os.Getenv("WINDOWS_PREFIX_PATH"),
		},
		Cmd: []string{"--", "agent", "clean"},
	}, &container.HostConfig{
		Binds:       binds,
		Privileged:  false,
		NetworkMode: "host",
	}, &network.NetworkingConfig{}, ContainerName)

	if err != nil {
		return err
	}

	return c.ContainerStart(ctx, container.ID, types.ContainerStartOptions{})
}

func Node() error {
	logrus.Info("Cleaning up node...")

	c, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}
	defer c.Close()

	ctx := context.Background()

	scriptBytes := []byte(PowershellScript)
	scriptPath := strings.Replace(getScriptPath(), "c:\\", "c:\\host\\", 1)
	if err := ioutil.WriteFile(scriptPath, scriptBytes, 0777); err != nil {
		return fmt.Errorf("error writing the cleanup script to the host: %s", err)
	}

	if err := waitForK8sPods(ctx, c); err != nil {
		return fmt.Errorf("error waiting for k8s pods to be removed: %s", err)
	}

	if err := stopContainers(ctx, c); err != nil {
		return fmt.Errorf("error trying to stop all rancher containers: %s", err)
	}

	if err := Paths(); err != nil {
		return fmt.Errorf("error trying to clean directories from the host: %s", err)
	}

	if err := cleanDocker(); err != nil {
		return fmt.Errorf("error trying to system prune docker: %s", err)
	}

	if err := Links(); err != nil {
		return fmt.Errorf("error trying to clean links from the host: %s", err)
	}

	if err := Firewall(); err != nil {
		return fmt.Errorf("error trying to flush firewall rules: %s", err)
	}

	return nil
}

func Links() error {
	winsArgs := createWinsArgs("Network")
	return exec.Command("wins.exe", winsArgs...).Run()
}

func Paths() error {
	winsArgs := createWinsArgs("Paths")
	return exec.Command("wins.exe", winsArgs...).Run()
}

func getAgentImage() string {
	agentImage := os.Getenv("AGENT_IMAGE")
	if agentImage == "" {
		agentImage = "rancher/rancher-agent:master"
	}
	return agentImage
}

func cleanDocker() error {
	winsArgs := createWinsArgs("Docker")
	return exec.Command("wins.exe", winsArgs...).Run()
}

func stopContainers(ctx context.Context, c *client.Client) error {
	containers, err := c.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return err
	}

	for _, container := range containers {
		config, err := c.ContainerInspect(ctx, container.ID)
		if err != nil {
			return err
		}
		if strings.HasPrefix(config.Config.Image, "rancher/") {
			if err := c.ContainerKill(ctx, config.ID, "SIGKILL"); err != nil {
				return err
			}
		}
	}

	return nil
}

func waitForK8sPods(ctx context.Context, c *client.Client) error {
	// wait for up to 5min for k8s pods to be dropped
	for i := 0; i < 30; i++ {
		logrus.Infof("checking for pods %d out of 30 times", i)
		containerList, err := c.ContainerList(ctx, types.ContainerListOptions{})
		if err != nil {
			return err
		}

		hasPods := false
		for _, c := range containerList {
			for _, n := range c.Names {
				if strings.HasPrefix(n, "/k8s_") {
					hasPods = true
					continue
				}
			}
			if hasPods {
				continue //break out if you already found one
			}
		}

		if hasPods {
			logrus.Info("pods found, waiting 10s and trying again")
			time.Sleep(10 * time.Second)
			continue
		}

		logrus.Info("all pods cleaned, continuing on to more rke cleanup")
		return nil
	}

	return nil
}

func Firewall() error {
	winsArgs := createWinsArgs("Firewall")
	return exec.Command("wins.exe", winsArgs...).Run()
}

func createWinsArgs(cmd string) []string {
	return []string{
		"cli",
		"prc",
		"run",
		"--path",
		fmt.Sprintf("\"%s\"", getScriptPath()),
		"--args",
		fmt.Sprintf("\"-Tasks %s\"", cmd),
	}
}

func getPrefixPath() string {
	return os.Getenv("WINDOWS_PREFIX_PATH")
}

func getScriptPath() string {
	return path.Join(getPrefixPath(), "etc", "rancher", "cleanup.ps1")
}
