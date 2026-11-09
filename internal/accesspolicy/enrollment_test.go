package accesspolicy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func enrollmentFixture(t *testing.T) (*Policy, Entry, EnrollmentDocument) {
	t.Helper()
	p, err := Parse(strings.NewReader(validPolicy()))
	if err != nil {
		t.Fatal(err)
	}
	e := p.Entries[0]
	e.Namespace = RuntimeNamespace(e.Source.Cluster, e.Source.Namespace)
	p.Entries = nil
	p.Enrollment = &Enrollment{ConfigMapName: "chat-ui-enrollment", Workspaces: []Workspace{{
		Cluster: e.Source.Cluster, Issuer: e.Issuer, ClientID: e.ClientID, ClientSecretName: e.ClientSecretName,
	}}}
	return p, e, EnrollmentDocument{Version: 1, Entries: []Entry{e}, Pins: []Entry{e}}
}

func enrollmentJSON(t *testing.T, doc EnrollmentDocument) string {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnrollmentBindsApprovedWorkspaceAndBothUIDs(t *testing.T) {
	p, e, doc := enrollmentFixture(t)
	obj := &metav1.ObjectMeta{UID: "local-uid", Namespace: e.Namespace, Name: e.Name,
		Annotations: map[string]string{"syncagent.kcp.io/remote-object-cluster": "victim", "slug": "victim"}}
	selected, err := p.SelectEnrolled(obj, strings.NewReader(enrollmentJSON(t, doc)))
	if err != nil || selected != e {
		t.Fatalf("verified binding not selected: %v", err)
	}
	obj.UID = "replacement-uid"
	if _, err := p.SelectEnrolled(obj, strings.NewReader(enrollmentJSON(t, doc))); !errors.Is(err, ErrUnmapped) {
		t.Fatal("replacement inherited the old managed UID's mapping")
	}
	if RuntimeNamespace("account-test", "default") != "chat-account-test-default-733cc4fc79ee2b87100c" {
		t.Fatal("naming differs from the pinned api-syncagent v0.7 template")
	}
}

func TestEnrollmentRejectsUnreviewedIdentityAndConfiguration(t *testing.T) {
	mutations := map[string]func(*EnrollmentDocument){
		"wrong workspace":         func(d *EnrollmentDocument) { d.Entries[0].Source.Cluster = "victim" },
		"wrong runtime namespace": func(d *EnrollmentDocument) { d.Entries[0].Namespace = "victim" },
		"wrong issuer":            func(d *EnrollmentDocument) { d.Entries[0].Issuer = "https://attacker.example.test" },
		"wrong credential":        func(d *EnrollmentDocument) { d.Entries[0].ClientSecretName = "portal-admin" },
		"missing pin":             func(d *EnrollmentDocument) { d.Pins = nil },
		"source recreation":       func(d *EnrollmentDocument) { d.Entries[0].Source.UID = "new-source-uid" },
		"duplicate managed UID":   func(d *EnrollmentDocument) { d.Entries = append(d.Entries, d.Entries[0]) },
		"unknown version":         func(d *EnrollmentDocument) { d.Version = 2 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p, _, doc := enrollmentFixture(t)
			mutate(&doc)
			if _, err := p.DecodeEnrollment(strings.NewReader(enrollmentJSON(t, doc))); err == nil {
				t.Fatal("unsafe enrollment accepted")
			}
		})
	}
	p, _, doc := enrollmentFixture(t)
	for _, text := range []string{enrollmentJSON(t, doc) + `{}`, `{"version":1,"entries":[],"pins":[],"unknown":true}`, strings.Repeat(" ", 1<<20) + enrollmentJSON(t, doc)} {
		if _, err := p.DecodeEnrollment(strings.NewReader(text)); err == nil {
			t.Fatal("malformed enrollment accepted")
		}
	}
}

func TestEnrollmentCannotOverrideStaticMapping(t *testing.T) {
	p, e, doc := enrollmentFixture(t)
	p.Entries = []Entry{e}
	if _, err := p.DecodeEnrollment(strings.NewReader(enrollmentJSON(t, doc))); err != nil {
		t.Fatal("identical reviewed static mapping should remain compatible")
	}
	p.Entries[0].Source.UID = "reviewed-different-source"
	obj := &metav1.ObjectMeta{UID: "local-uid", Namespace: e.Namespace, Name: e.Name}
	if _, err := p.SelectEnrolled(obj, strings.NewReader(enrollmentJSON(t, doc))); !errors.Is(err, ErrUnmapped) {
		t.Fatal("dynamic mapping replaced reviewed source identity")
	}
	p.Enrollment = nil
	if _, err := p.DecodeEnrollment(strings.NewReader(enrollmentJSON(t, doc))); !errors.Is(err, ErrUnmapped) {
		t.Fatal("enrollment was enabled by the document itself")
	}
}

func TestPolicyChangeRevokesOnlyAffectedEntryAndKeepsPinsReadable(t *testing.T) {
	for _, change := range []string{"remove workspace", "rotate credential"} {
		t.Run(change, func(t *testing.T) {
			p, entry, doc := enrollmentFixture(t)
			other := entry
			other.UID, other.Source.UID, other.Source.Cluster = "other-uid", "other-source", "other"
			other.Namespace = RuntimeNamespace(other.Source.Cluster, other.Source.Namespace)
			p.Enrollment.Workspaces = append(p.Enrollment.Workspaces, Workspace{Cluster: "other", Issuer: other.Issuer,
				ClientID: other.ClientID, ClientSecretName: other.ClientSecretName})
			doc.Entries, doc.Pins = append(doc.Entries, other), append(doc.Pins, other)
			if change == "remove workspace" {
				p.Enrollment.Workspaces = p.Enrollment.Workspaces[1:]
			} else {
				p.Enrollment.Workspaces[0].ClientSecretName = "new-credential"
			}
			raw := enrollmentJSON(t, doc)
			decoded, err := p.DecodeEnrollment(strings.NewReader(raw))
			if err != nil || len(decoded.Pins) != 2 {
				t.Fatalf("policy change prevented retaining identity history: %v", err)
			}
			for _, test := range []struct {
				entry   Entry
				allowed bool
			}{{entry, false}, {other, true}} {
				obj := &metav1.ObjectMeta{UID: types.UID(test.entry.UID), Namespace: test.entry.Namespace, Name: test.entry.Name}
				_, err := p.SelectEnrolled(obj, strings.NewReader(raw))
				if test.allowed && err != nil || !test.allowed && !errors.Is(err, ErrUnmapped) {
					t.Fatalf("wrong per-entry authorization after %s: %v", change, err)
				}
			}
		})
	}
}
