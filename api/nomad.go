package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/alytaphoenix/ratataskr/config"
)

type NomadClient struct {
	base  string
	token string
	http  *http.Client
}

type NomadJob struct {
	ID          string      `json:"ID"`
	Name        string      `json:"Name"`
	Type        string      `json:"Type"`
	Status      string      `json:"Status"`
	Priority    int         `json:"Priority"`
	Datacenters []string    `json:"Datacenters"`
	JobSummary  *JobSummary `json:"JobSummary"`
}

type JobSummary struct {
	Summary map[string]TaskGroupSummary `json:"Summary"`
}

type TaskGroupSummary struct {
	Queued   int `json:"Queued"`
	Complete int `json:"Complete"`
	Failed   int `json:"Failed"`
	Running  int `json:"Running"`
	Starting int `json:"Starting"`
	Lost     int `json:"Lost"`
	Unknown  int `json:"Unknown"`
}

func (j NomadJob) Totals() (running, failed, lost int) {
	if j.JobSummary == nil {
		return
	}
	for _, tg := range j.JobSummary.Summary {
		running += tg.Running
		failed += tg.Failed
		lost += tg.Lost
	}
	return
}

type NomadAllocation struct {
	ID            string `json:"ID"`
	JobID         string `json:"JobID"`
	TaskGroup     string `json:"TaskGroup"`
	NodeID        string `json:"NodeID"`
	NodeName      string `json:"NodeName"`
	ClientStatus  string `json:"ClientStatus"`
	DesiredStatus string `json:"DesiredStatus"`
	CreateTime    int64  `json:"CreateTime"` // nanoseconds since epoch
}

func NewNomadClient(cfg config.NomadConfig) *NomadClient {
	return &NomadClient{
		base:  strings.TrimRight(cfg.URL, "/") + "/v1",
		token: cfg.Token,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *NomadClient) nomadGet(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("X-Nomad-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *NomadClient) nomadDelete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("X-Nomad-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

func (c *NomadClient) Jobs(ctx context.Context) ([]NomadJob, error) {
	var jobs []NomadJob
	if err := c.nomadGet(ctx, "/jobs?meta=true", &jobs); err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].ID < jobs[j].ID
	})
	return jobs, nil
}

func (c *NomadClient) JobAllocations(ctx context.Context, jobID string) ([]NomadAllocation, error) {
	var allocs []NomadAllocation
	if err := c.nomadGet(ctx, "/job/"+jobID+"/allocations", &allocs); err != nil {
		return nil, err
	}
	// Most recent first
	sort.Slice(allocs, func(i, j int) bool {
		return allocs[i].CreateTime > allocs[j].CreateTime
	})
	return allocs, nil
}

func (c *NomadClient) StopJob(ctx context.Context, jobID string) error {
	return c.nomadDelete(ctx, "/job/"+jobID)
}
