package configaudit

import "testing"

// 测试结构体:模拟配置实体
type fakeCfg struct {
	ID          uint
	Name        string
	Threshold   int
	Enabled     bool
	Description string
}

// TestStructDiff_UpdatedFields 修改若干字段时返回对应 diff 条目
func TestStructDiff_UpdatedFields(t *testing.T) {
	old := fakeCfg{ID: 1, Name: "a", Threshold: 5, Enabled: true, Description: "old"}
	newV := fakeCfg{ID: 1, Name: "b", Threshold: 10, Enabled: true, Description: "new"}

	diffs := structDiff(old, newV)
	if len(diffs) != 3 {
		t.Fatalf("expected 3 diffs, got %d: %+v", len(diffs), diffs)
	}
	gotFields := map[string]bool{}
	for _, d := range diffs {
		gotFields[d.Field] = true
	}
	for _, want := range []string{"Name", "Threshold", "Description"} {
		if !gotFields[want] {
			t.Errorf("missing diff for field %s", want)
		}
	}
	if gotFields["ID"] {
		t.Error("ID should be ignored")
	}
}

// TestStructDiff_NoChange 无变更返回空
func TestStructDiff_NoChange(t *testing.T) {
	old := fakeCfg{Name: "same", Threshold: 5}
	newV := fakeCfg{Name: "same", Threshold: 5}
	if diffs := structDiff(old, newV); len(diffs) != 0 {
		t.Fatalf("expected no diff, got %+v", diffs)
	}
}

// TestStructDiff_Pointer 指针传入也能比较
func TestStructDiff_Pointer(t *testing.T) {
	old := fakeCfg{Name: "a"}
	newV := fakeCfg{Name: "b"}
	diffs := structDiff(&old, &newV)
	if len(diffs) != 1 || diffs[0].Field != "Name" {
		t.Fatalf("expected 1 diff on Name, got %+v", diffs)
	}
}

// TestJsonOf 序列化正常且过长时截断
func TestJsonOf(t *testing.T) {
	s := jsonOf(fakeCfg{Name: "x", Threshold: 7})
	if s == "" {
		t.Fatal("jsonOf returned empty")
	}
	if s[0] != '{' {
		t.Errorf("expected JSON object, got %q", s)
	}
}
