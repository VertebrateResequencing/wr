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

func TestLoadWorkflowFileRewritesAliasedWorkflowEmitRefs(t *testing.T) {
	Convey("LoadWorkflowFile rewrites workflow emit references for aliased imported processes", t, func() {
		tempDir := t.TempDir()
		workflowPath := filepath.Join(tempDir, "main.nf")
		moduleDir := filepath.Join(tempDir, "modules")
		modulePath := filepath.Join(moduleDir, "pack.nf")

		So(os.MkdirAll(moduleDir, 0o755), ShouldBeNil)
		So(os.WriteFile(workflowPath, []byte("include { helper as aliased ; pack } from './modules/pack.nf'\nworkflow { pack() }\n"), 0o644), ShouldBeNil)
		So(os.WriteFile(modulePath, []byte("process helper {\noutput:\nval 'hello', emit: greeting\nscript:\n'echo hello'\n}\nworkflow pack {\nhelper()\nemit:\nresult = helper.out.greeting\n}\n"), 0o644), ShouldBeNil)

		wf, err := LoadWorkflowFile(workflowPath, nil)
		So(err, ShouldBeNil)
		So(wf.SubWFs, ShouldHaveLength, 1)
		So(wf.SubWFs[0].Body.Emit, ShouldHaveLength, 1)
		So(wf.SubWFs[0].Body.Emit[0].Expr, ShouldEqual, "aliased.out.greeting")
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

func TestLoadWorkflowFilePreservesImportedProcessLabels(t *testing.T) {
	Convey("LoadWorkflowFile preserves labels on imported processes", t, func() {
		tempDir := t.TempDir()
		workflowPath := filepath.Join(tempDir, "main.nf")
		moduleDir := filepath.Join(tempDir, "modules")
		modulePath := filepath.Join(moduleDir, "helper.nf")

		So(os.MkdirAll(moduleDir, 0o755), ShouldBeNil)
		So(os.WriteFile(workflowPath, []byte("include { helper } from './modules/helper.nf'\nworkflow { helper() }\n"), 0o644), ShouldBeNil)
		So(os.WriteFile(modulePath, []byte("process helper {\nlabel 'big_mem'\nlabel 'long_time'\nscript:\n'echo hello'\n}\n"), 0o644), ShouldBeNil)

		wf, err := LoadWorkflowFile(workflowPath, nil)
		So(err, ShouldBeNil)
		So(wf.Processes, ShouldHaveLength, 1)
		So(wf.Processes[0].Name, ShouldEqual, "helper")
		So(wf.Processes[0].Labels, ShouldResemble, []string{"big_mem", "long_time"})
	})
}

func TestLoadWorkflowFilePreservesImportedWorkflowConditions(t *testing.T) {
	Convey("LoadWorkflowFile preserves conditional blocks on imported workflows", t, func() {
		tempDir := t.TempDir()
		workflowPath := filepath.Join(tempDir, "main.nf")
		moduleDir := filepath.Join(tempDir, "modules")
		modulePath := filepath.Join(moduleDir, "helper.nf")

		So(os.MkdirAll(moduleDir, 0o755), ShouldBeNil)
		So(os.WriteFile(workflowPath, []byte("include { helper } from './modules/helper.nf'\nworkflow { helper() }\n"), 0o644), ShouldBeNil)
		So(os.WriteFile(modulePath, []byte("process A {\nscript:\n'echo a'\n}\nprocess B {\nscript:\n'echo b'\n}\nworkflow helper {\nif( params.choice == 'a' ) {\nA()\n} else {\nB()\n}\n}\n"), 0o644), ShouldBeNil)

		wf, err := LoadWorkflowFile(workflowPath, nil)
		So(err, ShouldBeNil)
		So(wf.SubWFs, ShouldHaveLength, 1)
		So(wf.SubWFs[0].Body, ShouldNotBeNil)
		So(wf.SubWFs[0].Body.Conditions, ShouldHaveLength, 1)
		So(wf.SubWFs[0].Body.Conditions[0].Condition, ShouldEqual, "params.choice == 'a'")
		So(wf.SubWFs[0].Body.Conditions[0].Body, ShouldHaveLength, 1)
		So(wf.SubWFs[0].Body.Conditions[0].Body[0].Target, ShouldEqual, "A")
		So(wf.SubWFs[0].Body.Conditions[0].ElseBody, ShouldHaveLength, 1)
		So(wf.SubWFs[0].Body.Conditions[0].ElseBody[0].Target, ShouldEqual, "B")
	})
}

func TestLoadWorkflowFilePreservesImportedClosureParams(t *testing.T) {
	Convey("LoadWorkflowFile preserves parsed closure parameters in imported channel operators", t, func() {
		tempDir := t.TempDir()
		workflowPath := filepath.Join(tempDir, "main.nf")
		moduleDir := filepath.Join(tempDir, "modules")
		modulePath := filepath.Join(moduleDir, "helper.nf")

		So(os.MkdirAll(moduleDir, 0o755), ShouldBeNil)
		So(os.WriteFile(workflowPath, []byte("include { helper } from './modules/helper.nf'\nworkflow { helper() }\n"), 0o644), ShouldBeNil)
		So(os.WriteFile(modulePath, []byte("process sink {\ninput:\nval x\nscript:\n'echo ${x}'\n}\nworkflow helper {\nsink(Channel.of([id: 'a'], [id: 'b']).map { item -> item.id })\n}\n"), 0o644), ShouldBeNil)

		wf, err := LoadWorkflowFile(workflowPath, nil)
		So(err, ShouldBeNil)
		So(wf.SubWFs, ShouldHaveLength, 1)
		So(wf.SubWFs[0].Body, ShouldNotBeNil)
		So(wf.SubWFs[0].Body.Calls, ShouldHaveLength, 1)

		chain, ok := wf.SubWFs[0].Body.Calls[0].Args[0].(ChannelChain)
		So(ok, ShouldBeTrue)
		So(chain.Operators, ShouldHaveLength, 1)
		So(chain.Operators[0].Name, ShouldEqual, "map")
		So(chain.Operators[0].ClosureExpr, ShouldNotBeNil)
		So(chain.Operators[0].ClosureExpr.Params, ShouldResemble, []string{"item"})
		So(chain.Operators[0].ClosureExpr.Body, ShouldEqual, "item.id")
	})
}

func TestLoadWorkflowFilePreservesClonedProcessFields(t *testing.T) {
	Convey("LoadWorkflowFile preserves parsed process fields required by nextflowstrict translation", t, func() {
		tempDir := t.TempDir()
		workflowPath := filepath.Join(tempDir, "main.nf")

		workflow := `process demo {
input:
tuple val(id), path(reads)
output:
tuple val(id), path("${id}.bam"), emit: aligned, optional: true
script:
'touch ${id}.bam'
stub:
'echo stub'
exec:
"println 'exec'"
shell:
'echo !{id}'
when:
'params.run_demo'
}
workflow {
demo()
}
`

		So(os.WriteFile(workflowPath, []byte(workflow), 0o644), ShouldBeNil)

		wf, err := LoadWorkflowFile(workflowPath, nil)
		So(err, ShouldBeNil)
		So(wf.Processes, ShouldHaveLength, 1)

		proc := wf.Processes[0]
		So(proc.Input, ShouldHaveLength, 1)
		So(proc.Input[0].Kind, ShouldEqual, "tuple")
		So(proc.Input[0].Elements, ShouldHaveLength, 2)
		So(proc.Input[0].Elements[0].Kind, ShouldEqual, "val")
		So(proc.Input[0].Elements[0].Name, ShouldEqual, "id")
		So(proc.Input[0].Elements[1].Kind, ShouldEqual, "path")
		So(proc.Input[0].Elements[1].Name, ShouldEqual, "reads")

		So(proc.Output, ShouldHaveLength, 1)
		So(proc.Output[0].Kind, ShouldEqual, "tuple")
		So(proc.Output[0].Emit, ShouldEqual, "aligned")
		So(proc.Output[0].Optional, ShouldBeTrue)
		So(proc.Output[0].Elements, ShouldHaveLength, 2)
		So(proc.Output[0].Elements[0].Kind, ShouldEqual, "val")
		So(proc.Output[0].Elements[0].Name, ShouldEqual, "id")
		So(proc.Output[0].Elements[1].Kind, ShouldEqual, "path")
		stringExpr, ok := proc.Output[0].Elements[1].Expr.(StringExpr)
		So(ok, ShouldBeTrue)
		So(stringExpr.Value, ShouldEqual, "${id}.bam")

		So(proc.Stub, ShouldEqual, "echo stub")
		So(proc.Exec, ShouldEqual, "println 'exec'")
		So(proc.Shell, ShouldEqual, "echo !{id}")
		So(proc.When, ShouldEqual, "params.run_demo")
		So(proc.Labels, ShouldResemble, []string{})
	})
}
