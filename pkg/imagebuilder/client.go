package imagebuilder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/redhatinsights/edge-api/config"
	"github.com/redhatinsights/edge-api/pkg/models"
)

var Client ImageBuilderClientInterface

func InitClient() {
	Client = new(ImageBuilderClient)
}

// A lot of this code comes from https://github.com/osbuild/osbuild-composer

type OSTree struct {
	URL string `json:"url"`
	Ref string `json:"ref"`
}

type Customizations struct {
	Packages *[]string `json:"packages,omitempty"`
}

type UploadRequest struct {
	Options interface{} `json:"options"`
	Type    string      `json:"type"`
}

type UploadTypes string

type ImageRequest struct {
	Architecture  string         `json:"architecture"`
	ImageType     string         `json:"image_type"`
	Ostree        *OSTree        `json:"ostree,omitempty"`
	UploadRequest *UploadRequest `json:"upload_request"`
}

type ComposeRequest struct {
	Customizations *Customizations `json:"customizations,omitempty"`
	Distribution   string          `json:"distribution"`
	ImageRequests  []ImageRequest  `json:"image_requests"`
}

type ComposeStatus struct {
	ImageStatus ImageStatus `json:"image_status"`
}
type ImageStatus struct {
	Status       imageStatusValue `json:"status"`
	UploadStatus *UploadStatus    `json:"upload_status,omitempty"`
}

type imageStatusValue string

const (
	imageStatusBulding     imageStatusValue = "building"
	imageStatusFailure     imageStatusValue = "failure"
	imageStatusPending     imageStatusValue = "pending"
	imageStatusRegistering imageStatusValue = "registering"
	imageStatusSuccess     imageStatusValue = "success"
	imageStatusUploading   imageStatusValue = "uploading"
)

type UploadStatus struct {
	Options S3UploadStatus `json:"options"`
	Status  string         `json:"status"`
	Type    UploadTypes    `json:"type"`
}
type ComposeResult struct {
	Id string `json:"id"`
}

type S3UploadStatus struct {
	URL string `json:"url"`
}
type ImageBuilderClientInterface interface {
	ComposeCommit(image *models.Image, headers map[string]string) (*models.Image, error)
	ComposeInstaller(updateRecord *models.UpdateRecord, image *models.Image, headers map[string]string) (*models.Image, error)
	GetCommitStatus(image *models.Image, headers map[string]string) (*models.Image, error)
	GetInstallerStatus(image *models.Image, headers map[string]string) (*models.Image, error)
}

type ImageBuilderClient struct{}

func compose(composeReq *ComposeRequest, headers map[string]string) (*ComposeResult, error) {
	payloadBuf := new(bytes.Buffer)
	json.NewEncoder(payloadBuf).Encode(composeReq)
	cfg := config.Get()
	url := fmt.Sprintf("%s/v1/compose", cfg.ImageBuilderConfig.URL)
	log.Infof("Requesting url: %s", url)
	req, _ := http.NewRequest("POST", url, payloadBuf)
	for key, value := range headers {
		req.Header.Add(key, value)
	}
	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusCreated {
		body, _ := ioutil.ReadAll(res.Body)
		return nil, fmt.Errorf("error requesting image builder, got status code %d and body %s", res.StatusCode, body)
	}
	respBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	cr := &ComposeResult{}
	err = json.Unmarshal(respBody, &cr)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()
	return cr, nil
}

func (c *ImageBuilderClient) ComposeCommit(image *models.Image, headers map[string]string) (*models.Image, error) {
	req := &ComposeRequest{
		Customizations: &Customizations{
			Packages: image.Commit.GetPackagesList(),
		},

		Distribution: image.Distribution,
		ImageRequests: []ImageRequest{
			{
				Architecture: image.Commit.Arch,
				ImageType:    models.ImageTypeCommit,
				UploadRequest: &UploadRequest{
					Options: make(map[string]string),
					Type:    "aws.s3",
				},
			}},
	}
	if image.Commit.OSTreeRef != "" {
		if req.ImageRequests[0].Ostree == nil {
			req.ImageRequests[0].Ostree = &OSTree{}
		}
		req.ImageRequests[0].Ostree.Ref = image.Commit.OSTreeRef
	}
	if image.Commit.OSTreeRef != "" {
		if req.ImageRequests[0].Ostree == nil {
			req.ImageRequests[0].Ostree = &OSTree{}
		}
		req.ImageRequests[0].Ostree.URL = image.Commit.OSTreeParentCommit
	}

	cr, err := compose(req, headers)
	if err != nil {
		return nil, err
	}
	image.Commit.ComposeJobID = cr.Id
	image.Commit.Status = models.ImageStatusBuilding
	image.Status = models.ImageStatusBuilding
	return image, nil
}

func (c *ImageBuilderClient) ComposeInstaller(updateRecord *models.UpdateRecord, image *models.Image, headers map[string]string) (*models.Image, error) {
	var pkgs []string
	req := &ComposeRequest{
		Customizations: &Customizations{
			Packages: &pkgs,
		},

		Distribution: image.Distribution,
		ImageRequests: []ImageRequest{
			{
				Architecture: image.Commit.Arch,
				ImageType:    models.ImageTypeInstaller,
				Ostree: &OSTree{
					Ref: image.Commit.OSTreeRef,
					URL: fmt.Sprintf("http://s3httpproxy-env.eba-zswvuamp.us-east-2.elasticbeanstalk.com/%s/%d/repo", updateRecord.Account, updateRecord.ID),
				},
				UploadRequest: &UploadRequest{
					Options: make(map[string]string),
					Type:    "aws.s3",
				},
			}},
	}
	cr, err := compose(req, headers)
	if err != nil {
		return nil, err
	}
	image.Installer.ComposeJobID = cr.Id
	image.Installer.Status = models.ImageStatusBuilding
	image.Status = models.ImageStatusBuilding
	return image, nil
}

func getComposeStatus(jobId string, headers map[string]string) (*ComposeStatus, error) {
	cs := &ComposeStatus{}
	cfg := config.Get()
	url := fmt.Sprintf("%s/v1/composes/%s", cfg.ImageBuilderConfig.URL, jobId)
	req, _ := http.NewRequest("GET", url, nil)
	for key, value := range headers {
		req.Header.Add(key, value)
	}
	req.Header.Add("Content-Type", "application/json")
	log.Infof("Requesting url: %s", url)
	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(res.Body)
		return nil, fmt.Errorf("error requesting image builder, got status code %d and body %s", res.StatusCode, body)
	}
	respBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(respBody, &cs)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()
	return cs, nil
}

func (c *ImageBuilderClient) GetCommitStatus(image *models.Image, headers map[string]string) (*models.Image, error) {
	cs, err := getComposeStatus(image.Commit.ComposeJobID, headers)
	if err != nil {
		return nil, err
	}
	log.Info(fmt.Sprintf("Got UpdateCommitID status %s", cs.ImageStatus.Status))
	if cs.ImageStatus.Status == imageStatusSuccess {
		image.Status = models.ImageStatusSuccess
		image.Commit.Status = models.ImageStatusSuccess
		image.Commit.ImageBuildTarURL = cs.ImageStatus.UploadStatus.Options.URL
	} else if cs.ImageStatus.Status == imageStatusFailure {
		image.Commit.Status = models.ImageStatusError
		image.Status = models.ImageStatusError
	}
	return image, nil
}

func (c *ImageBuilderClient) GetInstallerStatus(image *models.Image, headers map[string]string) (*models.Image, error) {
	cs, err := getComposeStatus(image.Installer.ComposeJobID, headers)
	if err != nil {
		return nil, err
	}
	log.Info(fmt.Sprintf("Got installer status %s", cs.ImageStatus.Status))
	if cs.ImageStatus.Status == imageStatusSuccess {
		image.Status = models.ImageStatusSuccess
		image.Installer.Status = models.ImageStatusSuccess
		image.Installer.ImageBuildISOURL = cs.ImageStatus.UploadStatus.Options.URL
	} else if cs.ImageStatus.Status == imageStatusFailure {
		image.Installer.Status = models.ImageStatusError
		image.Status = models.ImageStatusError
	}
	return image, nil
}
