package jenkins

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// BuildRef names a build without necessarily knowing its number. The controller
// resolves these segment names natively, so supporting them costs nothing.
type BuildRef string

// Named build references accepted wherever a build is addressed.
const (
	RefLastBuild           BuildRef = "lastBuild"
	RefLastSuccessfulBuild BuildRef = "lastSuccessfulBuild"
	RefLastFailedBuild     BuildRef = "lastFailedBuild"
	RefLastCompletedBuild  BuildRef = "lastCompletedBuild"
)

func (r BuildRef) valid() bool {
	switch r {
	case RefLastBuild, RefLastSuccessfulBuild, RefLastFailedBuild, RefLastCompletedBuild:
		return true
	}
	if n, err := strconv.Atoi(string(r)); err == nil && n > 0 {
		return true
	}
	return false
}

func (r BuildRef) isNumber() bool {
	_, err := strconv.Atoi(string(r))
	return err == nil
}

// ParseBuildRef validates a user-supplied build reference.
func ParseBuildRef(s string) (BuildRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", NewError(CodeInvalidParameter, "build reference is empty", "")
	}
	ref := BuildRef(s)
	if !ref.valid() {
		return "", NewError(CodeInvalidParameter,
			fmt.Sprintf("unknown build reference %q", s),
			"use a build number or one of: lastBuild, lastSuccessfulBuild, lastFailedBuild, lastCompletedBuild")
	}
	return ref, nil
}

// JobPath converts a Full Name ("folder/sub/name") into the controller's path
// form ("/job/folder/job/sub/job/name").
//
// Folder support exists so that adding a folder later is not a breaking change to
// every tool signature, even though the reference controller has only top-level
// jobs today.
func JobPath(fullName string) (string, error) {
	fullName = strings.Trim(fullName, "/")
	if fullName == "" {
		return "", NewError(CodeInvalidParameter, "job name is empty", "")
	}
	segs := strings.Split(fullName, "/")
	var b strings.Builder
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return "", NewError(CodeInvalidParameter, "job name contains an empty segment", fullName)
		}
		if seg == "." || seg == ".." {
			return "", NewError(CodeInvalidParameter, "job name contains a relative segment", fullName)
		}
		b.WriteString("/job/")
		b.WriteString(url.PathEscape(seg))
	}
	return b.String(), nil
}

// buildPath is JobPath plus the build segment.
func buildPath(fullName string, ref BuildRef) (string, error) {
	jp, err := JobPath(fullName)
	if err != nil {
		return "", err
	}
	return jp + "/" + url.PathEscape(string(ref)), nil
}
