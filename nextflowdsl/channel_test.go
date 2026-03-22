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

		Convey("Channel.fromList expands list items like Channel.of", func() {
			items, err := ResolveChannel(ChannelFactory{Name: "fromList", Args: []Expr{ListExpr{Elements: []Expr{
				IntExpr{Value: 1},
				IntExpr{Value: 2},
				IntExpr{Value: 3},
			}}}}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{1, 2, 3})
		})

		Convey("Channel.from resolves like Channel.of and warns that it is deprecated", func() {
			var (
				items  []any
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				items, err = ResolveChannel(ChannelFactory{Name: "from", Args: []Expr{
					IntExpr{Value: 1},
					IntExpr{Value: 2},
					IntExpr{Value: 3},
				}}, "/work")
			})

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{1, 2, 3})
			So(stderr, ShouldContainSubstring, "deprecated channel factory \"from\"")
		})

		Convey("deprecated count operators warn and preserve the source item count", func() {
			for _, operatorName := range []string{"countFasta", "countFastq", "countJson", "countLines"} {
				name := operatorName
				Convey(name, func() {
					var (
						items  []any
						err    error
						stderr string
					)

					stderr = captureParseStderr(func() {
						items, err = ResolveChannel(ChannelChain{
							Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
							Operators: []ChannelOperator{{Name: name}},
						}, "/work")
					})

					So(err, ShouldBeNil)
					So(items, ShouldResemble, []any{1, 2, 3})
					So(stderr, ShouldContainSubstring, "deprecated channel operator")
					So(stderr, ShouldContainSubstring, name)
				})
			}
		})

		Convey("non-translatable factories warn and resolve to empty channels", func() {
			for _, factoryName := range []string{"fromSRA", "topic", "watchPath", "fromLineage", "interval"} {
				name := factoryName
				Convey(name, func() {
					var (
						items  []any
						err    error
						stderr string
					)

					stderr = captureParseStderr(func() {
						items, err = ResolveChannel(ChannelFactory{Name: name, Args: []Expr{StringExpr{Value: "ignored"}}}, "/work")
					})

					So(err, ShouldBeNil)
					So(items, ShouldBeEmpty)
					So(stderr, ShouldContainSubstring, fmt.Sprintf("channel factory %q cannot be translated", name))
				})
			}
		})
	})
}

func TestResolveChannelD6(t *testing.T) {
	Convey("ResolveChannel handles D6 operator resolution", t, func() {
		Convey("H2 simple closures evaluate during channel resolution", func() {
			Convey("map binds named parameters to channel items", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						MapExpr{Keys: []Expr{StringExpr{Value: "id"}}, Values: []Expr{StringExpr{Value: "a"}}},
						MapExpr{Keys: []Expr{StringExpr{Value: "id"}}, Values: []Expr{StringExpr{Value: "b"}}},
						MapExpr{Keys: []Expr{StringExpr{Value: "id"}}, Values: []Expr{StringExpr{Value: "c"}}},
					}},
					Operators: []ChannelOperator{{
						Name:        "map",
						Closure:     "item.id",
						ClosureExpr: &ClosureExpr{Params: []string{"item"}, Body: "item.id"},
					}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{"a", "b", "c"})
			})

			Convey("filter excludes falsy results from implicit it closures", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						IntExpr{Value: 1},
						IntExpr{Value: 5},
						IntExpr{Value: 2},
						IntExpr{Value: 7},
					}},
					Operators: []ChannelOperator{{
						Name:        "filter",
						Closure:     "it > 3",
						ClosureExpr: &ClosureExpr{Body: "it > 3"},
					}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{5, 7})
			})

			Convey("map replaces items with the evaluated closure result", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						IntExpr{Value: 1},
						IntExpr{Value: 2},
						IntExpr{Value: 3},
					}},
					Operators: []ChannelOperator{{
						Name:        "map",
						Closure:     "it * 2",
						ClosureExpr: &ClosureExpr{Body: "it * 2"},
					}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{2, 4, 6})
			})

			Convey("unsupported closures fall back to passthrough with a warning", func() {
				var (
					items  []any
					err    error
					stderr string
				)

				stderr = captureParseStderr(func() {
					items, err = ResolveChannel(ChannelChain{
						Source: ChannelFactory{Name: "of", Args: []Expr{
							IntExpr{Value: 1},
							IntExpr{Value: 2},
							IntExpr{Value: 3},
						}},
						Operators: []ChannelOperator{{
							Name:        "map",
							Closure:     "println it; it * 2",
							ClosureExpr: &ClosureExpr{Body: "println it; it * 2"},
						}},
					}, "/work")
				})

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{1, 2, 3})
				So(stderr, ShouldContainSubstring, "closure \"println it; it * 2\" could not be evaluated")
			})
		})

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

		Convey("view and dump are resolved as no-op debug operators", func() {
			items, err := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}}},
				Operators: []ChannelOperator{{Name: "view"}, {Name: "dump"}},
			}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{1, 2})
		})

		Convey("L1 cardinality-changing operators resolve with real semantics", func() {
			Convey("combine computes a Cartesian product", func() {
				resolver := func(ref ChanRef) ([]channelItem, error) {
					switch ref.Name {
					case "ch1":
						return []channelItem{{value: 1}, {value: 2}}, nil
					case "ch2":
						return []channelItem{{value: "a"}, {value: "b"}, {value: "c"}}, nil
					default:
						return nil, fmt.Errorf("unknown ref %s", ref.Name)
					}
				}

				items, err := resolveChannelItems(ChannelChain{
					Source:    ChanRef{Name: "ch1"},
					Operators: []ChannelOperator{{Name: "combine", Channels: []ChanExpr{ChanRef{Name: "ch2"}}}},
				}, "/work", resolver)

				So(err, ShouldBeNil)
				So(channelItemValues(items), ShouldResemble, []any{
					[]any{1, "a"}, []any{1, "b"}, []any{1, "c"},
					[]any{2, "a"}, []any{2, "b"}, []any{2, "c"},
				})
			})

			Convey("concat appends source and other channels in order", func() {
				resolver := func(ref ChanRef) ([]channelItem, error) {
					switch ref.Name {
					case "ch1":
						return []channelItem{{value: 1}, {value: 2}}, nil
					case "ch2":
						return []channelItem{{value: 3}, {value: 4}, {value: 5}}, nil
					default:
						return nil, fmt.Errorf("unknown ref %s", ref.Name)
					}
				}

				items, err := resolveChannelItems(ChannelChain{
					Source:    ChanRef{Name: "ch1"},
					Operators: []ChannelOperator{{Name: "concat", Channels: []ChanExpr{ChanRef{Name: "ch2"}}}},
				}, "/work", resolver)

				So(err, ShouldBeNil)
				So(channelItemValues(items), ShouldResemble, []any{1, 2, 3, 4, 5})
			})

			Convey("flatten expands nested list values into individual items", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						ListExpr{Elements: []Expr{IntExpr{Value: 1}, ListExpr{Elements: []Expr{IntExpr{Value: 2}, IntExpr{Value: 3}}}}},
						ListExpr{Elements: []Expr{IntExpr{Value: 4}, IntExpr{Value: 5}}},
					}},
					Operators: []ChannelOperator{{Name: "flatten"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{1, 2, 3, 4, 5})
			})

			Convey("unique preserves tuple uniqueness across all items", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						ListExpr{Elements: []Expr{IntExpr{Value: 1}, StringExpr{Value: "a"}}},
						ListExpr{Elements: []Expr{IntExpr{Value: 1}, StringExpr{Value: "b"}}},
						ListExpr{Elements: []Expr{IntExpr{Value: 2}, StringExpr{Value: "c"}}},
					}},
					Operators: []ChannelOperator{{Name: "unique"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{[]any{1, "a"}, []any{1, "b"}, []any{2, "c"}})
			})

			Convey("unique removes duplicate scalar items while preserving order", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 1}, IntExpr{Value: 3}, IntExpr{Value: 2},
					}},
					Operators: []ChannelOperator{{Name: "unique"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{1, 2, 3})
			})

			Convey("distinct removes only consecutive duplicates", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						IntExpr{Value: 1}, IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 2}, IntExpr{Value: 3}, IntExpr{Value: 1}, IntExpr{Value: 1},
					}},
					Operators: []ChannelOperator{{Name: "distinct"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{1, 2, 3, 1})
			})

			Convey("ifEmpty supplies a default item for empty channels", func() {
				items, err := ResolveChannel(ChannelChain{
					Source:    ChannelFactory{Name: "empty"},
					Operators: []ChannelOperator{{Name: "ifEmpty", Args: []Expr{StringExpr{Value: "default"}}}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{"default"})
			})

			Convey("ifEmpty keeps original items when the channel is non-empty", func() {
				items, err := ResolveChannel(ChannelChain{
					Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}}},
					Operators: []ChannelOperator{{Name: "ifEmpty", Args: []Expr{StringExpr{Value: "default"}}}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{1, 2})
			})

			Convey("toList collects items into a single list item", func() {
				items, err := ResolveChannel(ChannelChain{
					Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 3}, IntExpr{Value: 1}, IntExpr{Value: 2}}},
					Operators: []ChannelOperator{{Name: "toList"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{[]any{3, 1, 2}})
			})

			Convey("toSortedList collects and sorts items", func() {
				items, err := ResolveChannel(ChannelChain{
					Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 3}, IntExpr{Value: 1}, IntExpr{Value: 2}}},
					Operators: []ChannelOperator{{Name: "toSortedList"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{[]any{1, 2, 3}})
			})

			Convey("count returns the number of items as a singleton channel", func() {
				items, err := ResolveChannel(ChannelChain{
					Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
					Operators: []ChannelOperator{{Name: "count"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{3})
			})

			Convey("count supports a filter argument", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}, IntExpr{Value: 2}, IntExpr{Value: 4},
					}},
					Operators: []ChannelOperator{{Name: "count", Args: []Expr{IntExpr{Value: 2}}}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{2})
			})

			Convey("reduce folds items using the supplied closure", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3},
					}},
					Operators: []ChannelOperator{{
						Name:        "reduce",
						Closure:     "a + v",
						ClosureExpr: &ClosureExpr{Params: []string{"a", "v"}, Body: "a + v"},
					}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{6})
			})

			Convey("reduce supports an initial accumulator argument", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3},
					}},
					Operators: []ChannelOperator{{
						Name: "reduce",
						Args: []Expr{
							IntExpr{Value: 10},
							ClosureExpr{Params: []string{"a", "v"}, Body: "a + v"},
						},
					}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{16})
			})

			Convey("toFloat parses and resolves as a pass-through conversion", func() {
				wf, err := Parse(strings.NewReader("workflow { foo(Channel.of('1.5', '2.5').toFloat()) }"))

				So(err, ShouldBeNil)
				items, resolveErr := ResolveChannel(wf.EntryWF.Calls[0].Args[0], "/work")

				So(resolveErr, ShouldBeNil)
				So(items, ShouldResemble, []any{"1.5", "2.5"})
			})

			Convey("toLong parses and resolves as a pass-through conversion", func() {
				wf, err := Parse(strings.NewReader("workflow { foo(Channel.of('100', '200').toLong()) }"))

				So(err, ShouldBeNil)
				items, resolveErr := ResolveChannel(wf.EntryWF.Calls[0].Args[0], "/work")

				So(resolveErr, ShouldBeNil)
				So(items, ShouldResemble, []any{"100", "200"})
			})

			Convey("toDouble parses and resolves as a pass-through conversion", func() {
				wf, err := Parse(strings.NewReader("workflow { foo(Channel.of('1.5', '2.5').toDouble()) }"))

				So(err, ShouldBeNil)
				items, resolveErr := ResolveChannel(wf.EntryWF.Calls[0].Args[0], "/work")

				So(resolveErr, ShouldBeNil)
				So(items, ShouldResemble, []any{"1.5", "2.5"})
			})

			Convey("transpose expands nested list elements into separate items", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						ListExpr{Elements: []Expr{IntExpr{Value: 1}, ListExpr{Elements: []Expr{StringExpr{Value: "a"}, StringExpr{Value: "b"}}}}},
						ListExpr{Elements: []Expr{IntExpr{Value: 2}, ListExpr{Elements: []Expr{StringExpr{Value: "c"}}}}},
					}},
					Operators: []ChannelOperator{{Name: "transpose"}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{[]any{1, "a"}, []any{1, "b"}, []any{2, "c"}})
			})

			Convey("combine supports keyed cross-products via by", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						ListExpr{Elements: []Expr{IntExpr{Value: 1}, StringExpr{Value: "a"}}},
						ListExpr{Elements: []Expr{IntExpr{Value: 2}, StringExpr{Value: "b"}}},
					}},
					Operators: []ChannelOperator{{
						Name: "combine",
						Channels: []ChanExpr{ChannelFactory{Name: "of", Args: []Expr{
							ListExpr{Elements: []Expr{IntExpr{Value: 1}, StringExpr{Value: "x"}}},
							ListExpr{Elements: []Expr{IntExpr{Value: 1}, StringExpr{Value: "y"}}},
							ListExpr{Elements: []Expr{IntExpr{Value: 2}, StringExpr{Value: "z"}}},
						}}},
						Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "by"}}, Values: []Expr{IntExpr{Value: 0}}}},
					}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{[]any{1, "a", "x"}, []any{1, "a", "y"}, []any{2, "b", "z"}})
			})

			Convey("transpose supports explicit by indexes", func() {
				items, err := ResolveChannel(ChannelChain{
					Source: ChannelFactory{Name: "of", Args: []Expr{
						ListExpr{Elements: []Expr{IntExpr{Value: 1}, ListExpr{Elements: []Expr{StringExpr{Value: "a"}, StringExpr{Value: "b"}}}}},
						ListExpr{Elements: []Expr{IntExpr{Value: 2}, ListExpr{Elements: []Expr{StringExpr{Value: "c"}}}}},
					}},
					Operators: []ChannelOperator{{
						Name: "transpose",
						Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "by"}}, Values: []Expr{IntExpr{Value: 1}}}},
					}},
				}, "/work")

				So(err, ShouldBeNil)
				So(items, ShouldResemble, []any{[]any{1, "a"}, []any{1, "b"}, []any{2, "c"}})
			})
		})

		Convey("cross produces the Cartesian product across both channels", func() {
			resolver := func(ref ChanRef) ([]channelItem, error) {
				switch ref.Name {
				case "left":
					return []channelItem{{value: 1}, {value: 2}, {value: 3}}, nil
				case "right":
					return []channelItem{{value: "a"}, {value: "b"}}, nil
				default:
					return nil, fmt.Errorf("unknown ref %s", ref.Name)
				}
			}

			items, err := resolveChannelItems(ChannelChain{
				Source:    ChanRef{Name: "left"},
				Operators: []ChannelOperator{{Name: "cross", Channels: []ChanExpr{ChanRef{Name: "right"}}}},
			}, "/work", resolver)

			So(err, ShouldBeNil)
			So(channelItemValues(items), ShouldResemble, []any{
				[]any{1, "a"},
				[]any{1, "b"},
				[]any{2, "a"},
				[]any{2, "b"},
				[]any{3, "a"},
				[]any{3, "b"},
			})
		})

		Convey("buffer groups items according to the requested size", func() {
			items, err := ResolveChannel(ChannelChain{
				Source: ChannelFactory{Name: "of", Args: []Expr{
					IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}, IntExpr{Value: 4}, IntExpr{Value: 5},
					IntExpr{Value: 6}, IntExpr{Value: 7}, IntExpr{Value: 8}, IntExpr{Value: 9}, IntExpr{Value: 10},
				}},
				Operators: []ChannelOperator{{
					Name: "buffer",
					Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "size"}}, Values: []Expr{IntExpr{Value: 3}}}},
				}},
			}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{
				[]any{1, 2, 3},
				[]any{4, 5, 6},
				[]any{7, 8, 9},
				[]any{10},
			})
		})

		Convey("collate groups items into fixed-size chunks", func() {
			items, err := ResolveChannel(ChannelChain{
				Source: ChannelFactory{Name: "of", Args: []Expr{
					IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}, IntExpr{Value: 4}, IntExpr{Value: 5},
					IntExpr{Value: 6}, IntExpr{Value: 7}, IntExpr{Value: 8}, IntExpr{Value: 9}, IntExpr{Value: 10},
				}},
				Operators: []ChannelOperator{{Name: "collate", Args: []Expr{IntExpr{Value: 5}}}},
			}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{
				[]any{1, 2, 3, 4, 5},
				[]any{6, 7, 8, 9, 10},
			})
		})

		Convey("min reduces a channel to its minimum item", func() {
			items, err := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 3}, IntExpr{Value: 1}, IntExpr{Value: 4}, IntExpr{Value: 1}, IntExpr{Value: 5}}},
				Operators: []ChannelOperator{{Name: "min"}},
			}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{1})
		})

		Convey("max reduces a channel to its maximum item", func() {
			items, err := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 3}, IntExpr{Value: 1}, IntExpr{Value: 4}, IntExpr{Value: 1}, IntExpr{Value: 5}}},
				Operators: []ChannelOperator{{Name: "max"}},
			}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{5})
		})

		Convey("sum reduces a channel to the total of its items", func() {
			items, err := ResolveChannel(ChannelChain{
				Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
				Operators: []ChannelOperator{{Name: "sum"}},
			}, "/work")

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{6})
		})

		Convey("merge warns and preserves the source item count", func() {
			resolver := func(ref ChanRef) ([]channelItem, error) {
				switch ref.Name {
				case "left":
					return []channelItem{{value: 1}, {value: 2}, {value: 3}, {value: 4}, {value: 5}}, nil
				case "right":
					return []channelItem{{value: "a"}, {value: "b"}, {value: "c"}}, nil
				default:
					return nil, fmt.Errorf("unknown ref %s", ref.Name)
				}
			}

			var stderr string
			items := []channelItem{}
			var err error

			stderr = captureParseStderr(func() {
				items, err = resolveChannelItems(ChannelChain{
					Source:    ChanRef{Name: "left"},
					Operators: []ChannelOperator{{Name: "merge", Channels: []ChanExpr{ChanRef{Name: "right"}}}},
				}, "/work", resolver)
			})

			So(err, ShouldBeNil)
			So(channelItemValues(items), ShouldResemble, []any{1, 2, 3, 4, 5})
			So(stderr, ShouldContainSubstring, "deprecated channel operator \"merge\"")
		})

		Convey("toInteger warns and preserves the source item count", func() {
			var (
				items  []any
				err    error
				stderr string
			)

			stderr = captureParseStderr(func() {
				items, err = ResolveChannel(ChannelChain{
					Source:    ChannelFactory{Name: "of", Args: []Expr{IntExpr{Value: 1}, IntExpr{Value: 2}, IntExpr{Value: 3}}},
					Operators: []ChannelOperator{{Name: "toInteger"}},
				}, "/work")
			})

			So(err, ShouldBeNil)
			So(items, ShouldResemble, []any{1, 2, 3})
			So(stderr, ShouldContainSubstring, "deprecated channel operator \"toInteger\"")
		})
	})
}

func TestPendingChannelOperatorsL2(t *testing.T) {
	Convey("L2 data-dependent operators resolve concrete runtime items", t, func() {
		Convey("branch routes completed items into named output channels with distinct dep groups", func() {
			result, err := resolveBranchChannelItems([]channelItem{
				{value: 3, depGroups: []string{"up"}},
				{value: 15, depGroups: []string{"up"}},
				{value: 7, depGroups: []string{"up"}},
				{value: 20, depGroups: []string{"up"}},
			}, ChannelOperator{
				Name:        "branch",
				Closure:     "small: it < 10; big: it >= 10",
				ClosureExpr: &ClosureExpr{Body: "small: it < 10; big: it >= 10"},
			})

			So(err, ShouldBeNil)
			So(channelItemValues(result.items["small"]), ShouldResemble, []any{3, 7})
			So(channelItemValues(result.items["big"]), ShouldResemble, []any{15, 20})
			So(result.items["small"][0].depGroups[0], ShouldNotEqual, result.items["big"][0].depGroups[0])
		})

		Convey("multiMap emits one concrete item per named output channel", func() {
			result, err := resolveMultiMapChannelItems([]channelItem{{value: 5, depGroups: []string{"up"}}}, ChannelOperator{
				Name:        "multiMap",
				Closure:     "it -> foo: it * 2; bar: it + 1",
				ClosureExpr: &ClosureExpr{Params: []string{"it"}, Body: "foo: it * 2; bar: it + 1"},
			})

			So(err, ShouldBeNil)
			So(channelItemValues(result.items["foo"]), ShouldResemble, []any{10})
			So(channelItemValues(result.items["bar"]), ShouldResemble, []any{6})
			So(result.items["foo"][0].depGroups[0], ShouldNotEqual, result.items["bar"][0].depGroups[0])
		})

		Convey("splitCsv reads file data and emits one item per row", func() {
			path := filepath.Join(t.TempDir(), "data.csv")
			So(os.WriteFile(path, []byte("id,name\n1,alpha\n2,beta\n3,gamma\n"), 0o600), ShouldBeNil)

			items, err := splitCSVChannelItems([]channelItem{{value: path}}, ChannelOperator{
				Name: "splitCsv",
				Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "header"}}, Values: []Expr{BoolExpr{Value: true}}}},
			})

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, 3)
			So(items[0].value, ShouldResemble, map[string]any{"id": "1", "name": "alpha"})
			So(items[2].value, ShouldResemble, map[string]any{"id": "3", "name": "gamma"})
		})

		Convey("splitFasta reads sequences and emits one item per chunk", func() {
			path := filepath.Join(t.TempDir(), "reads.fa")
			So(os.WriteFile(path, []byte(">seq1\nACGT\n>seq2\nTGCA\n"), 0o600), ShouldBeNil)

			items, err := splitFASTAChannelItems([]channelItem{{value: path}}, ChannelOperator{
				Name: "splitFasta",
				Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "by"}}, Values: []Expr{IntExpr{Value: 1}}}},
			})

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, 2)
			So(items[0].value, ShouldEqual, ">seq1\nACGT")
			So(items[1].value, ShouldEqual, ">seq2\nTGCA")
		})

		Convey("collectFile writes completed items into a single collected file", func() {
			workDir := t.TempDir()
			items, err := collectFileChannelItems([]channelItem{{value: "alpha"}, {value: "beta"}, {value: "gamma"}}, ChannelOperator{
				Name: "collectFile",
				Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "name"}}, Values: []Expr{StringExpr{Value: "out.txt"}}}},
			}, workDir)

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, 1)
			So(items[0].value, ShouldEqual, filepath.Join(workDir, "out.txt"))

			content, readErr := os.ReadFile(filepath.Join(workDir, "out.txt"))
			So(readErr, ShouldBeNil)
			So(string(content), ShouldEqual, "alpha\nbeta\ngamma\n")
		})

		Convey("splitJson extracts items from the requested path", func() {
			path := filepath.Join(t.TempDir(), "data.json")
			So(os.WriteFile(path, []byte(`{"data":[{"id":1},{"id":2}]}`), 0o600), ShouldBeNil)

			items, err := splitJSONChannelItems([]channelItem{{value: path}}, ChannelOperator{
				Name: "splitJson",
				Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "path"}}, Values: []Expr{StringExpr{Value: "data"}}}},
			})

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, 2)
			So(items[0].value, ShouldResemble, map[string]any{"id": float64(1)})
			So(items[1].value, ShouldResemble, map[string]any{"id": float64(2)})
		})

		Convey("splitText chunks file lines according to the requested size", func() {
			lines := make([]string, 0, 25)
			for i := 1; i <= 25; i++ {
				lines = append(lines, fmt.Sprintf("line-%d", i))
			}

			path := filepath.Join(t.TempDir(), "data.txt")
			So(os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600), ShouldBeNil)

			items, err := splitTextChannelItems([]channelItem{{value: path}}, ChannelOperator{
				Name: "splitText",
				Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "by"}}, Values: []Expr{IntExpr{Value: 10}}}},
			})

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, 3)
			So(strings.Split(items[0].value.(string), "\n"), ShouldHaveLength, 10)
			So(strings.Split(items[1].value.(string), "\n"), ShouldHaveLength, 10)
			So(strings.Split(items[2].value.(string), "\n"), ShouldHaveLength, 5)
		})

		Convey("splitFastq pairs reads and emits one item per read pair", func() {
			workDir := t.TempDir()
			left := filepath.Join(workDir, "r1.fq")
			right := filepath.Join(workDir, "r2.fq")
			So(os.WriteFile(left, []byte("@r1/1\nACGT\n+\n!!!!\n@r2/1\nTGCA\n+\n####\n"), 0o600), ShouldBeNil)
			So(os.WriteFile(right, []byte("@r1/2\nTTTT\n+\n$$$$\n@r2/2\nCCCC\n+\n%%%%\n"), 0o600), ShouldBeNil)

			items, err := splitFASTQChannelItems([]channelItem{{value: []string{left, right}}}, ChannelOperator{
				Name: "splitFastq",
				Args: []Expr{MapExpr{Keys: []Expr{StringExpr{Value: "by"}, StringExpr{Value: "pe"}}, Values: []Expr{IntExpr{Value: 1}, BoolExpr{Value: true}}}},
			})

			So(err, ShouldBeNil)
			So(items, ShouldHaveLength, 2)
			firstPair, ok := items[0].value.([]any)
			So(ok, ShouldBeTrue)
			So(firstPair, ShouldHaveLength, 2)
			So(firstPair[0], ShouldEqual, "@r1/1\nACGT\n+\n!!!!")
			So(firstPair[1], ShouldEqual, "@r1/2\nTTTT\n+\n$$$$")
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
