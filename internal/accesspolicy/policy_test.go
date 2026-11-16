package accesspolicy

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validPolicy() string {
	return `{"kcpServer":"https://kcp.example.test","credentialsNamespace":"operator","proxyImage":"example.test/proxy@sha256:` + strings.Repeat("a", 64) + `","entries":[{"uid":"local-uid","namespace":"tenant","name":"chat","source":{"cluster":"account","namespace":"default","name":"chat","uid":"source-uid"},"issuer":"https://issuer.example.test/realms/org","clientID":"chat-ui","clientSecretName":"oidc-client"}]}`
}

func TestPolicyBindsImmutableLocalAndSourceIdentity(t *testing.T) {
	p, err := Parse(strings.NewReader(validPolicy()))
	if err != nil {
		t.Fatal(err)
	}
	obj := &metav1.ObjectMeta{UID: "local-uid", Namespace: "tenant", Name: "chat", Annotations: map[string]string{"source": "victim"}}
	e, err := p.Select(obj)
	if err != nil || e.Source.UID != "source-uid" {
		t.Fatal("mapping not selected")
	}
	obj.UID = "recreated"
	if _, err := p.Select(obj); !errors.Is(err, ErrUnmapped) {
		t.Fatal("new local UID inherited mapping")
	}
	obj.UID = "local-uid"
	obj.Namespace = "other"
	if _, err := p.Select(obj); !errors.Is(err, ErrUnmapped) {
		t.Fatal("foreign namespace inherited mapping")
	}
	var unset *Policy
	if _, err := unset.Select(obj); !errors.Is(err, ErrUnmapped) {
		t.Fatal("missing policy allowed access")
	}
}

func TestPolicyRejectsUnsafeAndAmbiguousConfiguration(t *testing.T) {
	base := validPolicy()
	cases := []string{`{}`, `null`, base + ` {}`, strings.Repeat(" ", 1<<20) + base,
		strings.Replace(base, `"kcpServer"`, `"typo"`, 1), strings.Replace(base, "https://kcp.example.test", "http://kcp.example.test", 1),
		strings.Replace(base, "https://kcp.example.test", "https://user:password@kcp.example.test", 1),
		strings.Replace(base, "https://kcp.example.test", "https://kcp.example.test/clusters/foreign", 1),
		strings.Replace(base, `"cluster":"account"`, `"cluster":"../../victim"`, 1),
		strings.Replace(base, `"uid":"source-uid"`, `"uid":""`, 1),
		strings.Replace(base, "@sha256:"+strings.Repeat("a", 64), ":latest", 1)}
	for _, input := range cases {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Fatal("accepted invalid access configuration")
		}
	}
}

func TestPasswordFallbackIsExplicitAndExcludesEnrollment(t *testing.T) {
	p, err := Parse(strings.NewReader(validPolicy()))
	if err != nil || p.PasswordFallback {
		t.Fatal("password fallback must be off unless configured")
	}
	enabled := strings.Replace(validPolicy(), `"entries":`, `"passwordFallback":true,"entries":`, 1)
	if p, err = Parse(strings.NewReader(enabled)); err != nil || !p.PasswordFallback {
		t.Fatalf("explicit password fallback rejected: %v", err)
	}
	enrollment := `"enrollment":{"configMapName":"chat-ui-enrollment","workspaces":[{"cluster":"account","issuer":"https://issuer.example.test/realms/org","clientID":"chat-ui","clientSecretName":"oidc-client"}]},`
	withEnrollment := strings.Replace(validPolicy(), `"entries":`, enrollment+`"entries":`, 1)
	if _, err := Parse(strings.NewReader(withEnrollment)); err != nil {
		t.Fatalf("enrollment fixture invalid: %v", err)
	}
	both := strings.Replace(enabled, `"entries":`, enrollment+`"entries":`, 1)
	if _, err := Parse(strings.NewReader(both)); err == nil {
		t.Fatal("password fallback combined with enrollment")
	}
}
