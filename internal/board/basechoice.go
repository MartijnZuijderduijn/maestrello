package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"maestrello/internal/gitops"
	"maestrello/internal/model"
)

// errNoAnswer means a question to me was never answered: the session or card ended first.
var errNoAnswer = errors.New("no answer: the session is ending")

// Answers to a "base" input.
const (
	baseLocal   = "local"   // branch from the local default branch
	baseOrigin  = "origin"  // branch from origin/<default>
	baseRetry   = "retry"   // try again (fetch included)
	baseInPlace = "inplace" // work in the original folder as local work
)

type baseOption struct {
	Answer string `json:"answer"`
	Label  string `json:"label"`
}

// gitCtx bounds one stretch of git work; waiting for me is never inside it.
func gitCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// chooseBase decides what a card's branch starts from: "main" or "origin/main", or inPlace
// when I chose to work in the original folder. It asks me unless the choice is obvious.
// failure is a git error from an earlier try; it is explained to me instead of the candidates.
func (a *App) chooseBase(ctx context.Context, card *model.Card, rel, orig string, failure error) (base string, inPlace bool, err error) {
	for {
		var info *gitops.BaseInfo
		if failure == nil {
			gctx, cancel := gitCtx()
			info, err = a.git.Bases(gctx, orig)
			cancel()
			if err != nil {
				failure = err
			} else if ref, ok := info.Auto(); ok {
				return ref, false, nil
			}
		}
		payload := baseQuestion(rel, info, failure)
		reply, ok := a.waitInput(ctx, card.ID, "base", payload)
		if !ok || reply.Stopped {
			return "", false, errNoAnswer
		}
		switch reply.Answer {
		case baseInPlace:
			return "", true, nil
		case baseRetry:
			failure = nil
		case baseLocal:
			if ref := a.localBase(orig, info); ref != "" {
				return ref, false, nil
			}
		case baseOrigin:
			if info != nil && info.Origin != "" {
				return info.Origin, false, nil
			}
		}
	}
}

// localBase is the local branch to start from: the default branch when known, else what is checked out.
func (a *App) localBase(orig string, info *gitops.BaseInfo) string {
	if info != nil {
		return info.Local
	}
	gctx, cancel := gitCtx()
	defer cancel()
	cur, _ := a.git.CurrentBranch(gctx, orig)
	return cur
}

// baseQuestion is the payload of a "base" input: what is wrong and which answers I have.
// info is nil when a git step failed (failure) before the candidates were known.
func baseQuestion(rel string, info *gitops.BaseInfo, failure error) map[string]any {
	p := map[string]any{"repo": rel}
	var msg string
	var opts []baseOption
	if info == nil {
		msg = fmt.Sprintf("Checking out %s failed: %v", rel, failure)
		opts = []baseOption{{baseRetry, "Retry"}, {baseLocal, "Use the local branch"}}
	} else {
		p["local"], p["origin"] = info.Local, info.Origin
		msg = baseMessage(rel, info)
		if info.Local != "" {
			opts = append(opts, baseOption{baseLocal, "Use local " + info.Local})
		}
		if info.Origin != "" {
			opts = append(opts, baseOption{baseOrigin, "Use " + info.Origin})
		}
		opts = append(opts, baseOption{baseRetry, "Retry the fetch"})
	}
	opts = append(opts, baseOption{baseInPlace, "Work in the original folder (local work)"})
	p["message"], p["options"] = msg, opts
	return p
}

// baseMessage explains why the base of rel is not obvious.
func baseMessage(rel string, info *gitops.BaseInfo) string {
	msg := fmt.Sprintf("Which base should the card branch of %s start from?", rel)
	if info.FetchErr != "" {
		msg += "\nFetching origin failed: " + info.FetchErr
	}
	switch {
	case info.Local == "":
		msg += "\nThere is no local default branch."
	case info.Origin == "":
		msg += fmt.Sprintf("\norigin has no %s.", info.Local)
	case info.Ahead > 0 && info.Behind > 0:
		msg += fmt.Sprintf("\n%s and %s have diverged: local is %d ahead and %d behind.", info.Local, info.Origin, info.Ahead, info.Behind)
	case info.Ahead > 0:
		msg += fmt.Sprintf("\nlocal %s is %d ahead of %s (unpushed commits).", info.Local, info.Ahead, info.Origin)
	case info.Behind > 0:
		msg += fmt.Sprintf("\nlocal %s is %d behind %s.", info.Local, info.Behind, info.Origin)
	default:
		msg += fmt.Sprintf("\nlocal %s and %s are the same commit.", info.Local, info.Origin)
	}
	if info.FetchErr != "" && info.Ahead == 0 && info.Behind == 0 && info.Local != "" && info.Origin != "" {
		msg += " origin may be out of date."
	}
	return msg
}

// validBaseAnswer reports whether answer is one of the options the input offered.
func validBaseAnswer(in *model.Input, answer string) bool {
	var p struct {
		Options []baseOption `json:"options"`
	}
	json.Unmarshal(in.Payload, &p)
	for _, o := range p.Options {
		if o.Answer == answer {
			return true
		}
	}
	return false
}
