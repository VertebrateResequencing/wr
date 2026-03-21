package nextflowdsl

import (
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