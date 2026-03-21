package nextflowdsl

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestCloneChanExpr(t *testing.T) {
	Convey("cloneChanExpr rewrites the most specific renamed reference first", t, func() {
		cloned := cloneChanExpr(ChanRef{Name: "helper_impl.out"}, map[string]string{
			"helper":      "aliased_helper",
			"helper_impl": "aliased_impl",
		})

		ref, ok := cloned.(ChanRef)
		So(ok, ShouldBeTrue)
		So(ref.Name, ShouldEqual, "aliased_impl.out")
	})

	Convey("cloneChanExpr preserves exact matches before broader prefixes", t, func() {
		cloned := cloneChanExpr(ChanRef{Name: "helper"}, map[string]string{
			"helper":      "aliased_helper",
			"helper_impl": "aliased_impl",
		})

		ref, ok := cloned.(ChanRef)
		So(ok, ShouldBeTrue)
		So(ref.Name, ShouldEqual, "aliased_helper")
	})
}

func TestLoadWorkflowFileRejectsImportCycles(t *testing.T) {
	Convey("LoadWorkflowFile returns an error for cyclic module includes", t, func() {
		tempDir := t.TempDir()
		workflowPath := filepath.Join(tempDir, "main.nf")
		moduleDir := filepath.Join(tempDir, "modules")
		helperPath := filepath.Join(moduleDir, "helper.nf")
		helperImplPath := filepath.Join(moduleDir, "helper_impl.nf")

		So(os.MkdirAll(moduleDir, 0o755), ShouldBeNil)
		So(os.WriteFile(workflowPath, []byte("include { helper } from './modules/helper.nf'\nworkflow { helper() }\n"), 0o644), ShouldBeNil)
		So(os.WriteFile(helperPath, []byte("include { helper_impl } from './helper_impl.nf'\nworkflow helper { helper_impl() }\n"), 0o644), ShouldBeNil)
		So(os.WriteFile(helperImplPath, []byte("include { helper } from './helper.nf'\nprocess helper_impl { script: 'echo hi' }\nworkflow helper_impl { helper() }\n"), 0o644), ShouldBeNil)

		_, err := LoadWorkflowFile(workflowPath, nil)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "workflow import cycle detected")
	})
}
