package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"

	"cloud.google.com/go/storage"
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
		newVersion, err := read(client, app.VersionBucket, applicationName)
		CheckIfError(err)
		if string(newVersion) == string(version) {
			sugar.Infow("no new updates for %s", applicationName)
			break
		}
		gitDirectory := fmt.Sprintf("tmp/%s/", applicationName)
		if _, err := os.Stat(gitDirectory); os.IsNotExist(err) {
			Info("git clone %s", app.DockerComposeURL)
			_, err := git.PlainClone(gitDirectory, false, &git.CloneOptions{
				URL:      app.DockerComposeURL,
				Progress: os.Stdout,
			})
			CheckIfError(err)
		}

		cmdStr := fmt.Sprintf("docker-compose -f %s.yml up -d", gitDirectory)
		out, err := exec.Command("/bin/sh", "-c", cmdStr).Output()
		CheckIfError(err)
		fmt.Printf("%s", out)
	}

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
	sugar.Infow("\x1b[34;1m%s\x1b[0m\n", fmt.Sprintf(format, args...))
}

func CheckIfError(err error) {
	if err == nil {
		return
	}
	sugar.Fatalf(err.Error())
}
