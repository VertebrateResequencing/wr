/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package nextflowdsl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestResolveChannelD5(t *testing.T) {
	Convey("ResolveChannel handles D5 factory resolution", t, func() {
		Convey("Channel.of resolves literal items", func() {
			items, err := ResolveChannel(ChannelFactory{Name: "of", Args: []Expr{
				IntExpr{Value: 1},
				IntExpr{Value: 2},
				IntExpr{Value: 3},
			}}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{1, 2, 3})
		})

		Convey("Channel.fromPath resolves matching files", func() {
			dataDir := t.TempDir()
			for _, name := range []string{"a.fq", "b.fq", "c.fq"} {
				err := os.WriteFile(filepath.Join(dataDir, name), []byte(name), 0o600)
				So(err, ShouldBeNil)
			}

			items, err := ResolveChannel(ChannelFactory{Name: "fromPath", Args: []Expr{StringExpr{Value: filepath.Join(dataDir, "*.fq")}}}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{
				filepath.Join(dataDir, "a.fq"),
				filepath.Join(dataDir, "b.fq"),
				filepath.Join(dataDir, "c.fq"),
			})
		})

		Convey("Channel.fromFilePairs groups matching mates into pairs", func() {
			dataDir := t.TempDir()
			for _, name := range []string{"sample1_1.fq", "sample1_2.fq", "sample2_1.fq", "sample2_2.fq"} {
				err := os.WriteFile(filepath.Join(dataDir, name), []byte(name), 0o600)
				So(err, ShouldBeNil)
			}

			items, err := ResolveChannel(ChannelFactory{Name: "fromFilePairs", Args: []Expr{StringExpr{Value: filepath.Join(dataDir, "*_{1,2}.fq")}}}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, 2)
			So(items[0], ShouldResemble, []string{filepath.Join(dataDir, "sample1_1.fq"), filepath.Join(dataDir, "sample1_2.fq")})
			So(items[1], ShouldResemble, []string{filepath.Join(dataDir, "sample2_1.fq"), filepath.Join(dataDir, "sample2_2.fq")})
		})

		Convey("Channel.value and Channel.empty resolve singleton and zero-item channels", func() {
			valueItems, valueErr := ResolveChannel(ChannelFactory{Name: "value", Args: []Expr{StringExpr{Value: "x"}}}, "/work")
			emptyItems, emptyErr := ResolveChannel(ChannelFactory{Name: "empty"}, "/work")

			So(valueErr, ShouldBeNil)
			So(valueItems, ShouldResemble, []any{"x"})
			So(emptyErr, ShouldBeNil)
			So(emptyItems, ShouldBeEmpty)
		})
	})
}

func TestResolveChannelD6(t *testing.T) {
	Convey("ResolveChannel handles D6 operator resolution", t, func() {
		Convey("collect, first, last, take, filter, map, and flatMap transform factory items", func() {
			collectItems, collectErr := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
				Operators: []ChannelOperator{{Name: "collect"}},
			}, "/work")
			firstItems, firstErr := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
				Operators: []ChannelOperator{{Name: "first"}},
			}, "/work")
			lastItems, lastErr := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
				Operators: []ChannelOperator{{Name: "last"}},
			}, "/work")
			takeItems, takeErr := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
				Operators: []ChannelOperator{{Name: "take", Args: []Expr{IntExpr{Value: 2}}}},
			}, "/work")
			filterItems, filterErr := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}, IntExpr{Value: 4}, IntExpr{Value: 5}}},
				Operators: []ChannelOperator{{Name: "filter", Closure: "it > 3"}},
			}, "/work")
			mapItems, mapErr := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
				Operators: []ChannelOperator{{Name: "map", Closure: "it * 2"}},
			}, "/work")
			flatMapItems, flatMapErr := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{StringExpr{Value: "a,b"}, StringExpr{Value: "c,d"}}},
				Operators: []ChannelOperator{{Name: "flatMap", Closure: "it.split(',')"}},
			}, "/work")

			So(collectErr, ShouldBeNil)
			So(collectItems, ShouldResemble, []any{[]any{1, 2, 3}})
			So(firstErr, ShouldBeNil)
			So(firstItems, ShouldResemble, []any{1})
			So(lastErr, ShouldBeNil)
			So(lastItems, ShouldResemble, []any{3})
			So(takeErr, ShouldBeNil)
			So(takeItems, ShouldResemble, []any{1, 2})
			So(filterErr, ShouldBeNil)
			So(filterItems, ShouldResemble, []any{4, 5})
			So(mapErr, ShouldBeNil)
			So(mapItems, ShouldResemble, []any{2, 4, 6})
			So(flatMapErr, ShouldBeNil)
			So(flatMapItems, ShouldResemble, []any{"a", "b", "c", "d"})
		})

		Convey("mix, join, and groupTuple transform referenced channels while preserving grouping", func() {
			resolver := func(ref ChanRef) ([]channelItem, error) {
				switch ref.Name {
				case "left":
					return []channelItem{{value: []any{"a", 1}}, {value: []any{"b", 2}}}, nil
				case "right":
					return []channelItem{{value: []any{"a", "x"}}, {value: []any{"b", "y"}}, {value: []any{"c", "z"}}}, nil
				case "pairs":
					return []channelItem{{value: []any{"a", 1}}, {value: []any{"a", 2}}, {value: []any{"b", 3}}}, nil
				default:
					return nil, fmt.Errorf("unknown ref %s", ref.Name)
				}
			}

			mixed, mixErr := resolveChannelItems(ChannelChain{
				Source:    ChanRef{Name: "left"},
				Operators: []ChannelOperator{{Name: "mix", Channels: []ChanExpr{ChanRef{Name: "right"}}}},
			}, "/work", resolver)
			joined, joinErr := resolveChannelItems(ChannelChain{
				Source:    ChanRef{Name: "left"},
				Operators: []ChannelOperator{{Name: "join", Channels: []ChanExpr{ChanRef{Name: "right"}}}},
			}, "/work", resolver)
			grouped, groupErr := resolveChannelItems(ChannelChain{
				Source:    ChanRef{Name: "pairs"},
				Operators: []ChannelOperator{{Name: "groupTuple"}},
			}, "/work", resolver)

			So(mixErr, ShouldBeNil)
			So(channelItemValues(mixed), ShouldResemble, []any{
				[]any{"a", 1},
				[]any{"b", 2},
				[]any{"a", "x"},
				[]any{"b", "y"},
				[]any{"c", "z"},
			})
			So(joinErr, ShouldBeNil)
			So(channelItemValues(joined), ShouldResemble, []any{
				[]any{"a", 1, "x"},
				[]any{"b", 2, "y"},
			})
			So(groupErr, ShouldBeNil)
			So(channelItemValues(grouped), ShouldResemble, []any{
				[]any{"a", []any{1, 2}},
				[]any{"b", []any{3}},
			})
		})

		Convey("parsed flatMap closures remain executable after parsing", func() {
			wf, err := Parse(strings.NewReader("workflow { foo(Channel.of('a,b', 'c,d').flatMap { it.split(',') }) }"))

			So(err, ShouldBeNil)
			So(wf.EntryWF, ShouldNotBeNil)
			So(wf.EntryWF.Calls, ShouldHaveLength, 1)

			items, resolveErr := ResolveChannel(wf.EntryWF.Calls[0].Args[0], "/work")

			So(resolveErr, ShouldBeNil)
			So(items, ShouldResemble, []any{"a", "b", "c", "d"})
		})
	})
}

func channelItemValues(items []channelItem) []any {
	values := make([]any, 0, len(items))
	for _, item := range items {
		values = append(values, item.value)
	}

	return values
}
