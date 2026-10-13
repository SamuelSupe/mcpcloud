package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOSSMetadataNeverExposesOwnerOrLifecyclePaths(t *testing.T) {
	acl, e := decodeOSSDepth("acl", 200, []byte(`<AccessControlPolicy><Owner><ID>sensitive-account</ID><DisplayName>sensitive-name</DisplayName></Owner><AccessControlList><Grant>private</Grant></AccessControlList></AccessControlPolicy>`))
	if e != nil || acl["acl"] != "private" {
		t.Fatalf("ACL result: %v %v", acl, e)
	}
	lifecycle, e := decodeOSSDepth("lifecycle", 200, []byte(`<LifecycleConfiguration><Rule><ID>sensitive-id</ID><Prefix>sensitive-path/</Prefix><Status>Enabled</Status></Rule><Rule><Status>Disabled</Status></Rule></LifecycleConfiguration>`))
	if e != nil || lifecycle["rule_count"] != 2 || lifecycle["enabled_rule_count"] != 1 {
		t.Fatalf("lifecycle result: %v %v", lifecycle, e)
	}
	for _, a := range []map[string]any{acl, lifecycle} {
		b, _ := json.Marshal(a)
		if strings.Contains(string(b), "sensitive") {
			t.Fatal("private configuration leaked")
		}
	}
}

func TestOSSAbsenceIsDistinctFromDeniedOrMalformed(t *testing.T) {
	absent, e := decodeOSSDepth("lifecycle", 404, []byte(`<Error><Code>NoSuchLifecycle</Code></Error>`))
	if e != nil || absent["configured"] != false {
		t.Fatal("explicit missing lifecycle should be verified empty")
	}
	for _, c := range []struct {
		kind   string
		status int
		body   string
	}{
		{"lifecycle", 403, `<Error><Code>AccessDenied</Code><Message>secret detail</Message></Error>`},
		{"lifecycle", 404, `<Error><Code>NoSuchBucket</Code></Error>`},
		{"acl", 200, `<AccessControlPolicy/>`},
		{"stat", 200, `<BucketStat><Storage>0</Storage></BucketStat>`},
		{"stat", 200, `<BucketStat><Storage>-1</Storage><ObjectCount>0</ObjectCount></BucketStat>`},
		{"versioning", 200, `<VersioningConfiguration><Status>unknown</Status></VersioningConfiguration>`},
	} {
		_, err := decodeOSSDepth(c.kind, c.status, []byte(c.body))
		if err == nil {
			t.Fatalf("bad response accepted: %s %d", c.kind, c.status)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("provider detail exposed")
		}
	}
	zero, e := decodeOSSDepth("stat", 200, []byte(`<BucketStat><Storage>0</Storage><ObjectCount>0</ObjectCount></BucketStat>`))
	if e != nil || zero["storage_bytes"] != int64(0) {
		t.Fatal("real zero must be retained")
	}
}
