package hostnamepolicy

import (
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const cutoff = "2026-10-26T00:00:00Z"

func TestValidatePublicHostIncludesLegacyLength(t *testing.T) {
	p, err := Parse(strings.NewReader(`{"cutover":"` + cutoff + `","entries":[
	{"uid":"uid","namespace":"tenant","name":"old","mode":"legacy","slug":"` + strings.Repeat("a", 63) + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// A 189-character suffix fits a 63-character legacy slug exactly (253).
	base := strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if err := p.ValidatePublicHost(base); err != nil {
		t.Fatal(err)
	}
	if _, err := Host(base+"d", "ui-"+strings.Repeat("a", 32)); err != nil {
		t.Fatal("fixture must still permit a derived name")
	}
	for _, invalid := range []string{base + "d", "", "example..test", strings.Repeat("x", 64) + ".test"} {
		if err := p.ValidatePublicHost(invalid); err == nil {
			t.Fatalf("accepted invalid public suffix: %q", invalid)
		}
	}
}

func TestSelectionUsesImmutableIdentity(t *testing.T) {
	p, err := Parse(strings.NewReader(`{"cutover":"` + cutoff + `","entries":[
	{"uid":"old-uid","namespace":"tenant","name":"old","mode":"legacy","slug":"kept-address"},
	{"uid":"never-provisioned","namespace":"tenant","name":"unused","mode":"derive"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	old := &metav1.ObjectMeta{Name: "old", Namespace: "tenant", UID: "old-uid",
		CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))}
	selected, err := p.Select(old)
	if err != nil || selected != (Selection{Slug: "kept-address", Source: Legacy}) {
		t.Fatalf("legacy selection: %+v, %v", selected, err)
	}
	old.Annotations = map[string]string{"ui.privatellms.msp/slug": "victim"}
	old.Labels = map[string]string{"ui.privatellms.msp/slug": "other"}
	after, err := p.Select(old)
	if err != nil || after != selected {
		t.Fatalf("tenant metadata changed the selected address: %+v, %v", after, err)
	}
	old.Namespace = "attacker"
	if _, err := p.Select(old); !errors.Is(err, ErrIdentity) {
		t.Fatalf("accepted mapping under wrong namespace: %v", err)
	}
	old.Namespace, old.Name, old.UID = "tenant", "new", "new-uid"
	if _, err := p.Select(old); !errors.Is(err, ErrLegacyUnmapped) {
		t.Fatalf("silently renamed unmapped legacy instance: %v", err)
	}
	old.CreationTimestamp = metav1.NewTime(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	if _, err := p.Select(old); !errors.Is(err, ErrLegacyUnmapped) {
		t.Fatalf("cutoff equality did not fail closed: %v", err)
	}
	old.CreationTimestamp.Time = old.CreationTimestamp.Add(time.Second)
	first, err := p.Select(old)
	if err != nil || first.Source != Derived || len(validation.IsDNS1123Label(first.Slug)) != 0 ||
		!reserved.MatchString(first.Slug) || strings.Contains(first.Slug, string(old.UID)) {
		t.Fatalf("new instance selection: %+v, %v", first, err)
	}
	again, err := p.Select(old)
	if err != nil || again != first {
		t.Fatalf("unstable selection: %+v, %v", again, err)
	}
	old.UID = "recreated-uid"
	if recreated, err := p.Select(old); err != nil || recreated.Slug == first.Slug {
		t.Fatalf("new UID retained previous identity: %+v, %v", recreated, err)
	}
	old.UID, old.Name = "never-provisioned", "unused"
	old.CreationTimestamp.Time = old.CreationTimestamp.Add(-time.Hour)
	if derived, err := p.Select(old); err != nil || derived.Source != Derived {
		t.Fatalf("explicit derive permission for legacy instance failed: %+v, %v", derived, err)
	}
}

func TestRejectInvalidPolicy(t *testing.T) {
	entry := `{"uid":"uid","namespace":"tenant","name":"demo","mode":"legacy","slug":"demo"}`
	cases := []string{
		`{}`, `null`, `{"cutover":"yesterday"}`, `{"cutover":"0001-01-01T00:00:00Z"}`,
		`{"cutover":"` + cutoff + `","typo":[]}`, `{"cutover":"` + cutoff + `"} {}`,
		`{"cutover":"` + cutoff + `","entries":[` + entry + `,` + entry + `]}`,
		`{"cutover":"` + cutoff + `"}` + strings.Repeat(" ", 1<<20) + `{}`,
	}
	for _, badEntry := range []string{
		strings.Replace(entry, `"uid":"uid"`, `"uid":""`, 1),
		strings.Replace(entry, `"namespace":"tenant"`, `"namespace":""`, 1),
		strings.Replace(entry, `"name":"demo"`, `"name":""`, 1),
		strings.Replace(entry, `"mode":"legacy"`, `"mode":"derive"`, 1),
		strings.Replace(entry, `"mode":"legacy"`, `"mode":"typo"`, 1),
		strings.Replace(entry, `"slug":"demo"`, `"slug":"UPPERCASE"`, 1),
		strings.Replace(entry, `"slug":"demo"`, `"slug":"ui-`+strings.Repeat("a", 32)+`"`, 1),
	} {
		cases = append(cases, `{"cutover":"`+cutoff+`","entries":[`+badEntry+`]}`)
	}
	otherUID := strings.Replace(entry, `"uid":"uid"`, `"uid":"other"`, 1)
	cases = append(cases, `{"cutover":"`+cutoff+`","entries":[`+entry+`,`+otherUID+`]}`)
	otherIdentity := strings.Replace(otherUID, `"namespace":"tenant"`, `"namespace":"other"`, 1)
	cases = append(cases, `{"cutover":"`+cutoff+`","entries":[`+entry+`,`+otherIdentity+`]}`)
	for _, input := range cases {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Errorf("accepted invalid config: %s", input)
		}
	}
}

func TestMissingRuntimeIdentityFailsClosed(t *testing.T) {
	p, err := Parse(strings.NewReader(`{"cutover":"` + cutoff + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []*metav1.ObjectMeta{{}, {UID: "uid"}} {
		if _, err := p.Select(inst); !errors.Is(err, ErrIdentity) {
			t.Fatalf("missing identity accepted: %v", err)
		}
	}
	var unset *Policy
	if _, err := unset.Select(&metav1.ObjectMeta{}); !errors.Is(err, ErrIdentity) {
		t.Fatalf("missing configuration accepted: %v", err)
	}
}
