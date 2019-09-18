package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/docker/libcompose/docker"
	"github.com/docker/libcompose/project"
	"github.com/docker/libcompose/project/options"
	"go.uber.org/zap"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/yaml.v2"
)

type Applications struct {
	Apps map[string]app `yaml:"apps"`
}

type app struct {
	DockerComposeURL string `yaml:"docker-compose-location"`
	VersionBucket    string `yaml:"version-bucket"`
	Frequency        string `yaml:"frequency"`
}

var version []byte
var sugar *zap.SugaredLogger

var NoDockerComposeError = errors.New("repo does not contain a docker compose file")
var LastMessage = func(format string, a ...interface{}) {
	fmt.Printf(format, a)
}

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync() // flushes buffer, if any
	sugar = logger.Sugar()

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		sugar.Fatalf("GOOGLE_CLOUD_PROJECT environment variable must be set.")
	}

	watchFile, err := ioutil.ReadFile("watch.yml")
	if err != nil {
		sugar.Fatalf("failed to read watch file")
	}
	var applications Applications
	err = yaml.Unmarshal(watchFile, &applications)
	if err != nil {
		sugar.Fatalf("failed to unmarshal watch file", "error", err.Error())
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	for applicationName, app := range applications.Apps {
		sugar.Infof("checking for latest version of %s", applicationName)
		messyVersion, err := read(client, app.VersionBucket, applicationName)
		if err != nil {
			sugar.Fatalf("can not get latest version b/c %s", err.Error())
		}
		newVersion := CleanNewVersion(messyVersion)
		if string(newVersion) == string(version) {
			sugar.Infow("no new updates for %s", applicationName)
			break
		}
		gitDirectory := fmt.Sprintf("tmp/%s", applicationName)
		if _, err := os.Stat(gitDirectory); os.IsNotExist(err) {
			Info("git clone %s", app.DockerComposeURL)
			_, err := git.PlainClone(gitDirectory, false, &git.CloneOptions{
				URL:      app.DockerComposeURL,
				Progress: os.Stdout,
			})
			CheckIfError(err)
		} else {
			r, err := git.PlainOpen(gitDirectory)
			CheckIfError(err)
			w, err := r.Worktree()
			CheckIfError(err)
			Info("git pull origin %s", gitDirectory)
			err = w.Pull(&git.PullOptions{RemoteName: "origin"})
			CheckIfError(err)
		}

		dockerFileLocation := fmt.Sprintf("%s/docker-compose.yml", gitDirectory)
		err = DoesDockerComposeFileExist(dockerFileLocation)
		if err != nil {
			sugar.Fatal(err)
		}

		project, err := docker.NewProject(&ctx.Context{
			Context: project.Context{
				ComposeFiles: []string{dockerFileLocation},
				ProjectName:  applicationName,
			},
		}, nil)

		if err != nil {
			log.Fatal(err)
		}

		err = project.Up(context.Background(), options.Up{})

		if err != nil {
			log.Fatal(err)
		}

		cmdArgs := []string{"-f", dockerFileLocation, "up", "-d"}
		dockerImageVersionTag := fmt.Sprintf("TAG=%s", string(newVersion))
		cmd := exec.Command("docker-compose", cmdArgs...)
		cmd.Env = append(cmd.Env, dockerImageVersionTag)
		Info("looking for docker image with version %s", string(newVersion))
		out, err := cmd.Output()
		if err != nil {
			sugar.Fatalf(string(err.(*exec.ExitError).Stderr))
		}
		CheckIfError(err)
		Info("%s", string(out))
	}
}

func CleanNewVersion(version []byte) string {
	return strings.TrimSuffix(string(version), "\n")
}

func DoesDockerComposeFileExist(dockerFileLocation string) error {
	if _, err := os.Stat(dockerFileLocation); os.IsNotExist(err) {
		return NoDockerComposeError
	}
	return nil
}

func read(client *storage.Client, bucket, object string) ([]byte, error) {
	ctx := context.Background()
	rc, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := ioutil.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func Info(format string, args ...interface{}) {
	sugar.Infow(fmt.Sprintf(format, args...))
}

func CheckIfError(err error) {
	if err == nil || IsNoErrAlreadyUpToDate(err) {
		return
	}
	sugar.Fatalf(err.Error())
}

func IsNoErrAlreadyUpToDate(err error) bool {
	return err.Error() == git.NoErrAlreadyUpToDate.Error()
}
