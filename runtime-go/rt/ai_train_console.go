//go:build !js

package rt

// ai_train_console.go — the training runs the Sky Console's AI panel shows.
//
// A row is read from a run directory (ai_train.go): its protocol lines folded
// into a status, steps, the losses (at most aiTrainConsolePoints points per
// curve) and the relaunches, and its result.json when it finished. The rows
// carry no dataset row, no prompt and no secret: a protocol line holds
// numbers, a checkpoint path and an error code with the driver's message.
//
// The runs shown are the ones this process touched (staged, launched,
// watched or read) and the ones in the default work directory `.sky/ai/runs`.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

const aiTrainConsolePoints = 200

// aiTrainRun is one training run in the panel.
type aiTrainRun struct {
	Run            string   `json:"run"`
	Dir            string   `json:"dir"`
	Status         string   `json:"status"`
	Backend        string   `json:"backend"`
	Step           int      `json:"step"`
	TotalSteps     int      `json:"totalSteps"`
	Loss           *float64 `json:"loss"`
	ValLoss        *float64 `json:"valLoss"`
	Attempt        int      `json:"attempt"`
	Relaunches     int      `json:"relaunches"`
	ResumedFrom    int      `json:"resumedFrom"`
	LastCheckpoint int      `json:"lastCheckpoint"`
	// The native trainer's memory in bytes: the highest peak it reported
	// (the guard's kill included) and its latest footprint; 0 when the
	// trainer reports none.
	PeakBytes      int64        `json:"peakBytes"`
	FootprintBytes int64        `json:"footprintBytes"`
	Message        string       `json:"message"`
	Registered     string       `json:"registered"`
	UpdatedMs      int64        `json:"updatedMs"`
	Losses         [][2]float64 `json:"losses"`
	ValLosses      [][2]float64 `json:"valLosses"`
	// A Vertex run: where it runs, the job's state and the spend so far
	// against the job's budget (exact money as text, "USD 0.31").
	Cloud      string `json:"cloud,omitempty"`
	CloudState string `json:"cloudState,omitempty"`
	Spent      string `json:"spent,omitempty"`
	Budget     string `json:"budget,omitempty"`
}

func aiThin(pts [][2]float64) [][2]float64 {
	if len(pts) <= aiTrainConsolePoints {
		return pts
	}
	out := make([][2]float64, 0, aiTrainConsolePoints)
	stride := float64(len(pts)-1) / float64(aiTrainConsolePoints-1)
	for i := 0; i < aiTrainConsolePoints; i++ {
		out = append(out, pts[int(float64(i)*stride+0.5)])
	}
	return out
}

// aiTrainConsoleRun folds a run directory into a panel row.
func aiTrainConsoleRun(dir string) aiTrainRun {
	r := aiTrainRun{Run: filepath.Base(dir), Dir: dir, Status: "queued", Losses: [][2]float64{}, ValLosses: [][2]float64{}}
	state := aiTrainReadState(dir)
	terminal := ""
	for _, l := range state["lines"].([]any) {
		var ev struct {
			Type       string   `json:"type"`
			Backend    string   `json:"backend"`
			Attempt    int      `json:"attempt"`
			ResumeFrom int      `json:"resumeFrom"`
			TotalSteps int      `json:"totalSteps"`
			Step       int      `json:"step"`
			Total      int      `json:"total"`
			Loss       *float64 `json:"loss"`
			Name       string   `json:"name"`
			Value      *float64 `json:"value"`
			ValLoss    *float64 `json:"valLoss"`
			Code       string   `json:"code"`
			Message    string   `json:"message"`
			Peak       int64    `json:"peakBytes"`
			Footprint  int64    `json:"footprintBytes"`
		}
		if json.Unmarshal([]byte(l.(string)), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "hello":
			r.Status, r.Backend, r.Attempt, terminal = "running", ev.Backend, ev.Attempt, ""
			if ev.Attempt > 0 {
				r.Relaunches++
				r.ResumedFrom = ev.ResumeFrom
			}
			if ev.TotalSteps > 0 {
				r.TotalSteps = ev.TotalSteps
			}
		case "progress":
			r.Step = ev.Step
			if ev.Footprint > 0 {
				r.FootprintBytes = ev.Footprint
			}
			r.PeakBytes = max(r.PeakBytes, ev.Peak)
			if ev.Total > 0 {
				r.TotalSteps = ev.Total
			}
			if ev.Loss != nil {
				r.Loss = ev.Loss
				r.Losses = append(r.Losses, [2]float64{float64(ev.Step), *ev.Loss})
			}
		case "metric":
			if ev.Name == "val_loss" && ev.Value != nil {
				r.ValLoss = ev.Value
				r.ValLosses = append(r.ValLosses, [2]float64{float64(ev.Step), *ev.Value})
			}
		case "checkpoint":
			r.LastCheckpoint = ev.Step
		case "done":
			terminal, r.Step = "converting", ev.Step
		case "cancelled":
			terminal, r.Step = "cancelled", ev.Step
		case "error":
			terminal, r.Message = "failed", ev.Code+": "+ev.Message
			r.PeakBytes = max(r.PeakBytes, ev.Peak)
		}
	}
	r.Losses, r.ValLosses = aiThin(r.Losses), aiThin(r.ValLosses)
	switch {
	case terminal == "converting" && state["converted"].(bool):
		r.Status = "registering"
	case terminal != "":
		r.Status = terminal
	case state["alive"].(bool):
		r.Status = "running"
	case !state["launched"].(bool):
		r.Status = "provisioning"
	case r.Status == "running":
		r.Status = "relaunching"
	}
	if res := state["result"].(string); res != "" {
		var out struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Adapter struct {
				Registered *string `json:"registered"`
			} `json:"adapter"`
		}
		if json.Unmarshal([]byte(res), &out) == nil {
			r.Status = out.Status
			if out.Status != "done" {
				r.Message = out.Message
				if r.Status == "failed" && aiFailureCode(out.Message) == string(aiTrainingCancelled) {
					r.Status = "cancelled"
				}
			}
			if out.Adapter.Registered != nil {
				r.Registered = *out.Adapter.Registered
			}
		}
	}
	if c := aiTrainReadCloud(dir); c.Job != "" {
		r.Cloud = "vertex " + c.Region + " " + c.Machine
		r.CloudState, r.Spent, r.Budget = c.State, c.Spent, c.Budget
		if r.Backend == "" {
			r.Backend = "vertex"
		}
	}
	for _, f := range []string{aiTrainResultFile, aiTrainEventsFile, aiTrainJobFile, aiTrainCloudFile} {
		if st, err := os.Stat(filepath.Join(dir, f)); err == nil && st.ModTime().UnixMilli() > r.UpdatedMs {
			r.UpdatedMs = st.ModTime().UnixMilli()
		}
	}
	return r
}

// aiFailureCode is the code a "Std.Ai <Code>: …" message starts with.
func aiFailureCode(msg string) string {
	const p = "Std.Ai "
	if len(msg) <= len(p) || msg[:len(p)] != p {
		return ""
	}
	rest := msg[len(p):]
	for i, c := range rest {
		if c == ':' {
			return rest[:i]
		}
	}
	return ""
}

// aiTrainConsoleRuns lists the panel's training runs, newest first.
func aiTrainConsoleRuns() []aiTrainRun {
	dirs := map[string]bool{}
	aiTrainSeen.Lock()
	for d := range aiTrainSeen.dirs {
		dirs[d] = true
	}
	aiTrainSeen.Unlock()
	for _, r := range aiTrainRunsIn(filepath.Join(".sky", "ai", "runs")) {
		if abs, err := filepath.Abs(r.dir); err == nil {
			dirs[abs] = true
		}
	}
	out := []aiTrainRun{}
	for d := range dirs {
		if fileExists(filepath.Join(d, aiTrainJobFile)) {
			out = append(out, aiTrainConsoleRun(d))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedMs != out[j].UpdatedMs {
			return out[i].UpdatedMs > out[j].UpdatedMs
		}
		return out[i].Run < out[j].Run
	})
	return out
}

// aiResetTrainConsole forgets the runs this process touched (tests).
func aiResetTrainConsole() {
	aiTrainSeen.Lock()
	aiTrainSeen.dirs = nil
	aiTrainSeen.Unlock()
}
