package aml

import (
	"bytes"
	"flag"
	"fmt"
	"gopheros/device/acpi/table"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

var (
	regenExpFiles = flag.Bool("aml-regenerate-parser-exp-files", false, "Regenerate the expected output files for AML parser tests against real AML files")
)

func TestParser(t *testing.T) {
	flag.Parse()

	pathToDumps := pkgDir() + "/../table/tabletest/"

	specs := []struct {
		expTreeContentFile string
		tableFiles         []string
	}{
		{
			"DSDT-SSDT.exp",
			[]string{"DSDT.aml", "SSDT.aml"},
		},
		{
			"parser-testsuite-DSDT.exp",
			[]string{"parser-testsuite-DSDT.aml"},
		},
	}

	for _, spec := range specs {
		t.Run(fmt.Sprintf("parse [%s]", strings.Join(spec.tableFiles, ", ")), func(t *testing.T) {
			var resolver = mockResolver{
				pathToDumps: pathToDumps,
				tableFiles:  spec.tableFiles,
			}

			tree := NewObjectTree()
			tree.CreateDefaultScopes(42)

			p := NewParser(&testWriter{t: t}, tree)
			for tableIndex, tableFile := range spec.tableFiles {
				tableName := strings.Replace(tableFile, ".aml", "", -1)
				if err := p.ParseAML(uint8(tableIndex), tableName, resolver.LookupTable(tableName)); err != nil {
					t.Errorf("[%s]: %v", tableName, err)
					return
				}
			}

			// Pretty-print tree
			var treeDump bytes.Buffer
			tree.PrettyPrint(&treeDump)

			// Check if we need to rebuild the exp files
			pathToExpFile := filepath.Join(pathToDumps, spec.expTreeContentFile)
			if *regenExpFiles {
				f, err := os.Create(pathToExpFile)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = f.Close() }()

				if _, err = treeDump.WriteTo(f); err != nil {
					t.Fatal(err)
				}
				t.Logf("regenerated exp file contents: %s", pathToExpFile)
				return
			}

			// Read the exp file and compare its contents to the generated dump
			expDump, err := ioutil.ReadFile(pathToExpFile)
			if err != nil {
				t.Fatalf("error opening exp file: %s; try running tests with -aml-regenerate-parser-exp-files", pathToExpFile)
			}

			if !reflect.DeepEqual(expDump, treeDump.Bytes()) {
				t.Fatal("parsed tree content does not match expected content")
			}
		})
	}
}

func TestParseAMLErrors(t *testing.T) {
	t.Run("parseObjectList failed", func(t *testing.T) {
		p, resolver := parserForMockPayload(t, []byte{uint8(pOpBuffer)})
		if err := p.ParseAML(0, "DSDT", resolver.LookupTable("DSDT")); err != errParsingAML {
			t.Fatalf("expected to get errParsingAML; got: %v", err)
		}
	})

	t.Run("connectNamedObjArgs failed", func(t *testing.T) {
		p, resolver := parserForMockPayload(t, []byte{})

		namedObj := p.objTree.newNamedObject(pOpName, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		p.objTree.append(namedObj, p.objTree.newObject(pOpDwordPrefix, 0))
		p.objTree.append(p.objTree.ObjectAt(1), namedObj) // Attach to first child of root scope

		if err := p.ParseAML(0, "DSDT", resolver.LookupTable("DSDT")); err != errParsingAML {
			t.Fatalf("expected to get errParsingAML; got: %v", err)
		}
	})

	t.Run("mergeScopeDirectives failed", func(t *testing.T) {
		p, resolver := parserForMockPayload(t, []byte{})

		scopeDirective := p.objTree.newObject(pOpScope, 0)
		p.objTree.append(p.objTree.ObjectAt(1), scopeDirective) // Attach to first child of root scope

		if err := p.ParseAML(0, "DSDT", resolver.LookupTable("DSDT")); err != errParsingAML {
			t.Fatalf("expected to get errParsingAML; got: %v", err)
		}
	})

	t.Run("relocateNamedObjects failed", func(t *testing.T) {
		p, resolver := parserForMockPayload(t, []byte{})

		namedObj := p.objTree.newNamedObject(pOpName, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		namepath := p.objTree.newObject(pOpIntNamePath, 0)
		namepath.value = []byte{'^', '^', 'F', 'O', 'O', 'F'}
		target := p.objTree.newObject(pOpOnes, 0)
		p.objTree.append(namedObj, namepath)
		p.objTree.append(namedObj, target)
		p.objTree.append(p.objTree.ObjectAt(0), namedObj)

		if err := p.ParseAML(0, "DSDT", resolver.LookupTable("DSDT")); err != errParsingAML {
			t.Fatalf("expected to get errParsingAML; got: %v", err)
		}
	})

	t.Run("parseDeferredBlocks failed", func(t *testing.T) {
		p, resolver := parserForMockPayload(t, []byte{})

		// Attach a deferred block to the first child of the root scope
		def := p.objTree.newObject(pOpBankField, 0)
		def.pkgEnd = 1
		p.objTree.append(p.objTree.ObjectAt(1), def)

		if err := p.ParseAML(0, "DSDT", resolver.LookupTable("DSDT")); err != errParsingAML {
			t.Fatalf("expected to get errParsingAML; got: %v", err)
		}
	})

	t.Run("resolveMethodCalls failed", func(t *testing.T) {
		p, resolver := parserForMockPayload(t, []byte{})

		method := p.objTree.newNamedObject(pOpMethod, 0, [amlNameLen]byte{'M', 'T', 'H', 'D'})
		namepath := p.objTree.newObject(pOpIntNamePath, 0)
		namepath.value = []byte{'M', 'T', 'H', 'D'}
		p.objTree.append(method, namepath)
		p.objTree.append(p.objTree.ObjectAt(0), method)

		inv := p.objTree.newObject(pOpIntNamePathOrMethodCall, 0)
		inv.value = []byte{'M', 'T', 'H', 'D'}
		p.objTree.append(p.objTree.ObjectAt(0), inv)

		if err := p.ParseAML(0, "DSDT", resolver.LookupTable("DSDT")); err != errParsingAML {
			t.Fatalf("expected to get errParsingAML; got: %v", err)
		}
	})

	t.Run("connectNonNamedObjArgs  failed", func(t *testing.T) {
		p, resolver := parserForMockPayload(t, []byte{})

		// Use a named object whose args contain a TermArg
		obj := p.objTree.newObject(pOpMatch, 0)
		p.objTree.append(p.objTree.ObjectAt(0), obj)

		if err := p.ParseAML(0, "DSDT", resolver.LookupTable("DSDT")); err != errParsingAML {
			t.Fatalf("expected to get errParsingAML; got: %v", err)
		}
	})
}

func TestParseObjectListErrors(t *testing.T) {
	p, _ := parserForMockPayload(t, []byte{uint8(pOpBuffer)})
	p.scopeEnter(0)
	if res := p.parseObjectList(); res != parseResultFailed {
		t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
	}
}

func TestParseArgErrors(t *testing.T) {
	info := new(pOpcodeInfo)
	obj := new(Object)

	t.Run("parsePkgLength error", func(t *testing.T) {
		// Incomplete pkg length
		p, _ := parserForMockPayload(t, []byte{0xff})
		if _, res := p.parseArg(info, obj, pArgTypePkgLen); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("invalid pkgLen", func(t *testing.T) {
		p, _ := parserForMockPayload(t, []byte{0x4})
		if _, res := p.parseArg(info, obj, pArgTypePkgLen); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("parseTermList error", func(t *testing.T) {
		p, _ := parserForMockPayload(t, []byte{extOpPrefix})
		p.mode = parseModeAllBlocks
		if _, res := p.parseArg(info, obj, pArgTypeTermList); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})
}

func TestParseNameOrMethodCallErrors(t *testing.T) {
	payload := []byte{
		// Namestring
		'F', 'O', 'O', 'F',
	}

	t.Run("path not resolving to entity", func(t *testing.T) {
		p, _ := parserForMockPayload(t, payload)
		p.scopeEnter(0)

		p.mode = parseModeAllBlocks
		if res := p.parseNamePathOrMethodCall(); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("incomplete method call", func(t *testing.T) {
		p, _ := parserForMockPayload(t, payload)
		p.mode = parseModeAllBlocks

		// Add a method FOOF object as a child of the root scope and set it up
		// so that it requires 1 arg.
		method := p.objTree.newNamedObject(pOpMethod, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		p.objTree.append(p.objTree.ObjectAt(0), method)

		p.objTree.append(method, p.objTree.newObject(pOpIntNamePath, 0))
		argCountObj := p.objTree.newObject(pOpBytePrefix, 0)
		argCountObj.value = uint64(1)
		p.objTree.append(method, argCountObj)

		// Enter one of the default child scopes of the root scope
		// so the lookup for FOOF yields the object we just appended.
		p.scopeEnter(1)

		p.mode = parseModeAllBlocks
		if res := p.parseNamePathOrMethodCall(); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})
}

func TestParseStrictTermArgErrors(t *testing.T) {
	// Set up the stream to include a non-Type2/arg opcode
	p, _ := parserForMockPayload(t, []byte{uint8(pOpMethod)})
	p.scopeEnter(0)

	p.mode = parseModeAllBlocks
	if _, res := p.parseStrictTermArg(new(Object)); res != parseResultFailed {
		t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
	}
}

func TestParseSimpleArg(t *testing.T) {
	specs := []struct {
		payload []byte
		argType pArgType
		expRes  parseResult
		expVal  interface{}
	}{
		{
			[]byte{0x32},
			pArgTypeByteData,
			parseResultOk,
			uint64(0x32),
		},
		{
			[]byte{0x32, 0x33},
			pArgTypeWordData,
			parseResultOk,
			uint64(0x3332),
		},
		{
			[]byte{0x32, 0x33, 0x34, 0x35},
			pArgTypeDwordData,
			parseResultOk,
			uint64(0x35343332),
		},
		{
			[]byte{0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39},
			pArgTypeQwordData,
			parseResultOk,
			uint64(0x3938373635343332),
		},
		{
			[]byte{'F', 'O', 'O', 0x00},
			pArgTypeString,
			parseResultOk,
			[]byte{'F', 'O', 'O'},
		},
		{
			[]byte{'^', '^', 'F', 'O', 'O', 'F'},
			pArgTypeNameString,
			parseResultOk,
			[]byte{'^', '^', 'F', 'O', 'O', 'F'},
		},
		// unsupported arg type
		{
			[]byte{},
			pArgTypeFieldList,
			parseResultFailed,
			nil,
		},
	}

	for specIndex, spec := range specs {
		p, _ := parserForMockPayload(t, spec.payload)
		obj, res := p.parseSimpleArg(spec.argType)
		if res != spec.expRes {
			t.Errorf("[spec %d] expected to get parse result %d; got %d", specIndex, spec.expRes, res)
			continue
		}

		if obj != nil && !reflect.DeepEqual(obj.value, spec.expVal) {
			t.Errorf("[spec %d] expected to get value \"%v\"; got \"%v\"", specIndex, spec.expVal, obj.value)
		}
	}
}

func TestParseTargetErrors(t *testing.T) {
	// Set up the stream to include an un-expected opcode
	p, _ := parserForMockPayload(t, []byte{uint8(pOpMethod)})
	p.scopeEnter(0)

	if _, res := p.parseTarget(); res != parseResultFailed {
		t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
	}
}

func TestParseFieldElementsErrors(t *testing.T) {
	genFieldObj := func(tree *ObjectTree) *Object {
		field := tree.newObject(pOpField, 0)
		flags := tree.newObject(pOpBytePrefix, 0)
		flags.value = uint64(0xfe)
		tree.append(field, flags)
		tree.append(tree.ObjectAt(0), field)
		return field
	}

	specs := []struct {
		descr   string
		payload []byte
	}{
		{
			"reserved field parsePkgLength error",
			[]byte{
				0x00, // ReservedField
			},
		},
		{
			"access field access type parse error",
			[]byte{
				0x01, // AccessField
			},
		},
		{
			"access field access attrib parse error",
			[]byte{
				0x01, // AccessField
				0x01, // AccessType
			},
		},
		{
			"ext. access field access type parse error",
			[]byte{
				0x03, // ExtendedAccessField
			},
		},
		{
			"ext. access field access attrib parse error",
			[]byte{
				0x03, // ExtendedAccessField
				0x01, // AccessType
			},
		},
		{
			"ext. access field access length parse error",
			[]byte{
				0x03, // ExtendedAccessField
				0x01, // AccessType
				0x02, // AccessAttrib
			},
		},
		{
			"EOF parsing connection",
			[]byte{
				0x02, // Connection
			},
		},
		{
			"connection buffer data parsePkgLength error",
			[]byte{
				0x02, // Connection
				uint8(pOpBuffer),
			},
		},
		{
			"connection buffer length exceeds stream length",
			[]byte{
				0x02, // Connection
				uint8(pOpBuffer),
				0x5, // pkgLen
			},
		},
		{
			"connection bad opcode type for buffer size",
			[]byte{
				0x02, // Connection
				uint8(pOpBuffer),
				0x1, // pkgLen
				extOpPrefix,
			},
		},
		{
			"connection bad word buffer size",
			[]byte{
				0x02, // Connection
				uint8(pOpBuffer),
				0x2, // pkgLen
				uint8(pOpWordPrefix),
				0x1, // missing next byte
			},
		},
		{
			"connection bad dword buffer size",
			[]byte{
				0x02, // Connection
				uint8(pOpBuffer),
				0x3, // pkgLen
				uint8(pOpDwordPrefix),
				0x1, 0x2, 0x3, // missing next byte
			},
		},
		{
			"connection parse namestring error",
			[]byte{
				0x02, // Connection
				'^',
			},
		},
		{
			"incomplete named field name",
			[]byte{
				'F', 'O', 'O', // missing last character
			},
		},
		{
			"error parsing named field pkgLength",
			[]byte{
				'F', 'O', 'O', 'F',
				0xff, // incomplete pkg length
			},
		},
	}

	for _, spec := range specs {
		t.Run(spec.descr, func(t *testing.T) {
			p, _ := parserForMockPayload(t, spec.payload)
			if res := p.parseFieldElements(genFieldObj(p.objTree)); res != parseResultFailed {
				t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
			}
		})
	}
}

func TestParsePkgLength(t *testing.T) {
	specs := []struct {
		payload []byte
		exp     uint32
	}{
		// lead byte bits (6:7) indicate 1 extra byte for the len. The
		// parsed length will use bits 0:3 from the lead byte plus
		// the full 8 bits of the following byte.
		{
			[]byte{1<<6 | 7, 255},
			4087,
		},
		// lead byte bits (6:7) indicate 2 extra bytes for the len. The
		// parsed length will use bits 0:3 from the lead byte plus
		// the full 8 bits of the following bytes.
		{
			[]byte{2<<6 | 8, 255, 128},
			528376,
		},
		// lead byte bits (6:7) indicate 3 extra bytes for the len. The
		// parsed length will use bits 0:3 from the lead byte plus
		// the full 8 bits of the following bytes.
		{
			[]byte{3<<6 | 6, 255, 128, 42},
			44568566,
		},
	}

	for specIndex, spec := range specs {
		p, _ := parserForMockPayload(t, spec.payload)
		got, res := p.parsePkgLength()

		if res != parseResultOk {
			t.Errorf("[spec %d] expected to get parseResultOk(%d); got %d", specIndex, parseResultOk, res)
			continue
		}

		if got != spec.exp {
			t.Errorf("[spec %d] expected parsePkgLength to return %d; got %d", specIndex, spec.exp, got)
		}
	}
}

func TestParsePkgLengthErrors(t *testing.T) {
	specs := [][]byte{
		// lead byte bits (6:7) indicate 1 extra byte that is missing
		[]byte{1 << 6},
		// lead byte bits (6:7) indicate 2 extra bytes with the 1st and then 2nd missing
		[]byte{2 << 6},
		[]byte{2 << 6, 0x1},
		// lead byte bits (6:7) indicate 3 extra bytes with the 1st and then 2nd and then 3rd missing
		[]byte{3 << 6},
		[]byte{3 << 6, 0x1},
		[]byte{3 << 6, 0x1, 0x2},
	}

	for specIndex, spec := range specs {
		p, _ := parserForMockPayload(t, spec)
		if _, res := p.parsePkgLength(); res != parseResultFailed {
			t.Errorf("[spec %d] expected to get parseResultFailed(%d); got %d", specIndex, parseResultFailed, res)
		}
	}
}

func TestParseStringErrors(t *testing.T) {
	specs := [][]byte{
		// Unexpected EOF before terminating null byte
		[]byte{'A'},
		// Characters outside the allowed [0x01, 0x7f] range
		[]byte{'A', 0xba, 0xdf, 0x00},
	}

	for specIndex, spec := range specs {
		p, _ := parserForMockPayload(t, spec)

		if _, res := p.parseString(); res != parseResultFailed {
			t.Errorf("[spec %d] expected to get parseResultFailed(%d); got %d", specIndex, parseResultFailed, res)
		}
	}
}

func TestParseNamestring(t *testing.T) {
	specs := []struct {
		payload []byte
		expRes  parseResult
		expVal  []byte
	}{
		{
			[]byte{0x00},
			parseResultOk,
			[]byte{},
		},
		{
			// NameSeg with root prefix
			[]byte{'\\', '_', 'F', 'O', 'O'},
			parseResultOk,
			[]byte{'\\', '_', 'F', 'O', 'O'},
		},
		{
			// DualNamePath
			[]byte{0x2e, 'F', 'O', 'O', 'F', 'B', 'A', 'R', 'B'},
			parseResultOk,
			[]byte{0x2e, 'F', 'O', 'O', 'F', 'B', 'A', 'R', 'B'},
		},
		{
			// MultiNamePath with caret prefix
			[]byte{'^', '^',
				0x2f,
				0x3, // 3 segments
				'F', 'O', 'O', 'F',
				'F', 'O', 'O', 'F',
				'F', 'O', 'O', 'F',
			},
			parseResultOk,
			[]byte{'^', '^',
				0x2f,
				0x3, // 3 segments
				'F', 'O', 'O', 'F',
				'F', 'O', 'O', 'F',
				'F', 'O', 'O', 'F',
			},
		},
		{
			// Unexpected EOF after prefix
			[]byte{'^'},
			parseResultFailed,
			nil,
		},
		{
			// Unexpected EOF in DualNamePath
			[]byte{0x2e, 'F', 'O', 'O', 'F', 'B', 'A', 'R'}, // missing last char
			parseResultFailed,
			nil,
		},
		{
			// Unexpected EOF reading SegCount in MultiNamePath
			[]byte{'^', '^',
				0x2f,
				// missing segment count
			},
			parseResultFailed,
			nil,
		},
		{
			// Unexpected EOF in MultiNamePath
			[]byte{'^', '^',
				0x2f,
				0x3, // 3 segments
				'F', 'O', 'O', 'F',
				'F', 'O', 'O', 'F',
				// missing third segment
			},
			parseResultFailed,
			nil,
		},
		{
			// Unexpected EOF in NameSeg
			[]byte{'F', 'O', 'O'}, // missing last char
			parseResultFailed,
			nil,
		},
		{
			// Invalid lead char for NameSeg
			[]byte{'0', 'F', 'O', 'O'},
			parseResultFailed,
			nil,
		},
	}

	for specIndex, spec := range specs {
		p, _ := parserForMockPayload(t, spec.payload)
		obj, res := p.parseNameString()
		if res != spec.expRes {
			t.Errorf("[spec %d] expected to get parse result %d; got %d", specIndex, spec.expRes, res)
			continue
		}

		if obj != nil && !reflect.DeepEqual(obj, spec.expVal) {
			t.Errorf("[spec %d] expected to get value \"%v\"; got \"%v\"", specIndex, spec.expVal, obj)
		}
	}
}

func TestConnectNamedObjectsErrors(t *testing.T) {
	t.Run("first arg is not a namepath", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		namedObj := tree.newNamedObject(pOpName, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		tree.append(namedObj, tree.newObject(pOpDwordPrefix, 0))
		tree.append(tree.ObjectAt(1), namedObj) // Attach to first child of root scope

		p := NewParser(ioutil.Discard, tree)
		if res := p.connectNamedObjArgs(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("named object arg count mismatch", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		// Use a named object whose args contain a TermArg
		namedObj := tree.newNamedObject(pOpBankField, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		namepathObj := tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'F', 'O', 'O', 'F'}
		tree.append(namedObj, namepathObj)
		tree.append(tree.ObjectAt(0), namedObj)

		p := NewParser(ioutil.Discard, tree)
		if res := p.connectNamedObjArgs(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})
}

func TestMergeScopeDirectivesErrors(t *testing.T) {
	t.Run("malformed scope object", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		scopeDirective := tree.newObject(pOpScope, 0)
		tree.append(tree.ObjectAt(1), scopeDirective) // Attach to first child of root scope

		p := NewParser(ioutil.Discard, tree)
		if res := p.mergeScopeDirectives(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("unable to resolve scope reference", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		scopeDirective := tree.newObject(pOpScope, 0)
		namepathObj := tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'F', 'O', 'O', 'F'}
		tree.append(scopeDirective, namepathObj)
		tree.append(tree.ObjectAt(1), scopeDirective) // Attach to first child of root scope

		// Simulate second mergeScopes attempt
		p := NewParser(ioutil.Discard, tree)
		p.resolvePasses = 2

		if res := p.mergeScopeDirectives(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("scope target resolves to obj without IntScopeBlock child", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		scopeDirective := tree.newObject(pOpScope, 0)
		namepathObj := tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'D', 'E', 'V', '0'}
		tree.append(scopeDirective, namepathObj)
		tree.append(tree.ObjectAt(1), scopeDirective) // Attach to first child of root scope

		tree.append(tree.ObjectAt(0), tree.newNamedObject(pOpDevice, 0, [amlNameLen]byte{'D', 'E', 'V', '0'}))

		// Simulate second mergeScopes attempt
		p := NewParser(ioutil.Discard, tree)
		p.resolvePasses = 2

		if res := p.mergeScopeDirectives(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})
}

func TestRelocateNamedObjectsErrors(t *testing.T) {
	t.Run("first arg is not a namepath", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		namedObj := tree.newNamedObject(pOpName, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		tree.append(namedObj, tree.newObject(pOpDwordPrefix, 0))
		tree.append(tree.ObjectAt(1), namedObj) // Attach to first child of root scope

		p := NewParser(ioutil.Discard, tree)
		if res := p.relocateNamedObjects(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("unresolved relocation target", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		scope := tree.newNamedObject(pOpIntScopeBlock, 0, [amlNameLen]byte{'S', 'C', 'O', 'P'})
		tree.append(tree.ObjectAt(1), scope)

		namedObj := tree.newNamedObject(pOpName, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		namepathObj := tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'^', '^', '^', '^', 'F', 'O', 'O', 'F'}
		tree.append(namedObj, namepathObj)
		tree.append(scope, namedObj)

		p := NewParser(ioutil.Discard, tree)
		if res := p.relocateNamedObjects(0); res != parseResultRequireExtraPass {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultRequireExtraPass, res)
		}
	})

	t.Run("unresolved relocation target after maxResolvePasses", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		namedObj := tree.newNamedObject(pOpName, 0, [amlNameLen]byte{'F', 'O', 'O', 'F'})
		namepathObj := tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'^', '^', 'F', 'O', 'O', 'F'}
		tree.append(namedObj, namepathObj)

		// call relocateNamedObjects on detached nameObj and simulate maxResolvePasses relocateNamedObjects calls
		p := NewParser(ioutil.Discard, tree)
		p.resolvePasses = maxResolvePasses + 1

		if res := p.relocateNamedObjects(namedObj.index); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("resolve target resolves to obj without IntScopeBlock child", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		dev0 := tree.newNamedObject(pOpDevice, 0, [amlNameLen]byte{'D', 'E', 'V', '0'})
		namepathObj := tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'D', 'E', 'V', '0'}
		tree.append(dev0, namepathObj)
		tree.append(tree.ObjectAt(0), dev0)

		dev1 := tree.newNamedObject(pOpDevice, 0, [amlNameLen]byte{'D', 'E', 'V', '1'})
		namepathObj = tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'^', 'D', 'E', 'V', '1'}
		tree.append(dev1, namepathObj)

		// place another named obj between dev0 and dev1 so that the caret in
		// dev1 namepath resolves to dev0
		cpu0 := tree.newNamedObject(pOpProcessor, 0, [amlNameLen]byte{'C', 'P', 'U', '0'})
		namepathObj = tree.newObject(pOpIntNamePath, 0)
		namepathObj.value = []byte{'C', 'P', 'U', '0'}
		tree.append(cpu0, namepathObj)
		tree.append(dev0, cpu0)
		tree.append(cpu0, dev1)

		p := NewParser(ioutil.Discard, tree)
		if res := p.relocateNamedObjects(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})
}

func TestParseDeferredBlocksErrors(t *testing.T) {
	p, _ := parserForMockPayload(t, []byte{extOpPrefix})

	// Attach a deferred block to the first child of the root scope
	def := p.objTree.newObject(pOpBankField, 0)
	def.pkgEnd = 1
	p.objTree.append(p.objTree.ObjectAt(1), def)

	if res := p.parseDeferredBlocks(0); res != parseResultFailed {
		t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
	}
}

func TestConnectNonNamedObjectsErrors(t *testing.T) {
	// Allocate a tree but don't create default scopes to make sure that we lack
	// the number of args (siblings OR parent's siblings) required for the pOpAdd opcode
	tree := NewObjectTree()
	root := tree.newObject(pOpIntScopeBlock, 0)
	scope := tree.newObject(pOpIntScopeBlock, 0)
	tree.append(root, scope)

	// Use a named object whose args contain a TermArg
	obj := tree.newObject(pOpAdd, 0)
	tree.append(scope, obj)

	p := NewParser(os.Stdout, tree)
	if res := p.connectNonNamedObjArgs(0); res != parseResultFailed {
		t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
	}
}

func TestResolveMethodCallsErrors(t *testing.T) {
	t.Run("method missing flag object", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		method := tree.newNamedObject(pOpMethod, 0, [amlNameLen]byte{'M', 'T', 'H', 'D'})
		namepath := tree.newObject(pOpIntNamePath, 0)
		namepath.value = []byte{'M', 'T', 'H', 'D'}
		tree.append(method, namepath)
		tree.append(tree.ObjectAt(0), method)

		inv := tree.newObject(pOpIntNamePathOrMethodCall, 0)
		inv.value = []byte{'M', 'T', 'H', 'D'}
		tree.append(tree.ObjectAt(0), inv)

		p := NewParser(os.Stdout, tree)
		if res := p.resolveMethodCalls(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("method contains malformed flag object", func(t *testing.T) {
		tree := NewObjectTree()
		tree.CreateDefaultScopes(0)

		method := tree.newNamedObject(pOpMethod, 0, [amlNameLen]byte{'M', 'T', 'H', 'D'})
		namepath := tree.newObject(pOpIntNamePath, 0)
		namepath.value = []byte{'M', 'T', 'H', 'D'}
		flags := tree.newObject(pOpStringPrefix, 0)
		flags.value = []byte{'F', 'O', 'O'} // malformed flags: this should be a uint64
		tree.append(method, namepath)
		tree.append(method, flags)
		tree.append(tree.ObjectAt(0), method)

		inv := tree.newObject(pOpIntNamePathOrMethodCall, 0)
		inv.value = []byte{'M', 'T', 'H', 'D'}
		tree.append(tree.ObjectAt(0), inv)

		p := NewParser(os.Stdout, tree)
		if res := p.resolveMethodCalls(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})

	t.Run("method call arg count mismatch", func(t *testing.T) {
		// Allocate a tree but don't create default scopes to make sure that we lack
		// the number of args (siblings OR parent's siblings) required for the method
		// invocation
		tree := NewObjectTree()
		root := tree.newObject(pOpIntScopeBlock, 0)
		scope := tree.newObject(pOpIntScopeBlock, 0)
		tree.append(root, scope)

		// MTHD calls expect 6 args
		method := tree.newNamedObject(pOpMethod, 0, [amlNameLen]byte{'M', 'T', 'H', 'D'})
		namepath := tree.newObject(pOpIntNamePath, 0)
		namepath.value = []byte{'M', 'T', 'H', 'D'}
		flags := tree.newObject(pOpBytePrefix, 0)
		flags.value = uint64(0x6)
		tree.append(method, namepath)
		tree.append(method, flags)
		tree.append(scope, method)

		inv := tree.newObject(pOpIntNamePathOrMethodCall, 0)
		inv.value = []byte{'M', 'T', 'H', 'D'}
		tree.append(scope, inv)

		p := NewParser(os.Stdout, tree)
		if res := p.resolveMethodCalls(0); res != parseResultFailed {
			t.Fatalf("expected to get parseResultFailed(%d); got %d", parseResultFailed, res)
		}
	})
}

func parserForMockPayload(t *testing.T, payload []byte) (*Parser, table.Resolver) {
	tree := NewObjectTree()
	tree.CreateDefaultScopes(42)
	p := NewParser(&testWriter{t: t}, tree)

	resolver := mockByteDataResolver(payload)

	p.init(0, "DSDT", resolver.LookupTable("DSDT"))
	return p, resolver
}

type testWriter struct {
	t   *testing.T
	buf bytes.Buffer
}

func (t *testWriter) Write(data []byte) (int, error) {
	for _, b := range data {
		if b == '\n' {
			t.t.Log(t.buf.String())
			t.buf.Reset()
			continue
		}
		_ = t.buf.WriteByte(b)
	}

	return len(data), nil
}

type mockByteDataResolver []byte

func (m mockByteDataResolver) LookupTable(string) *table.SDTHeader {
	headerLen := unsafe.Sizeof(table.SDTHeader{})
	stream := make([]byte, int(headerLen)+len(m))
	copy(stream[headerLen:], m)

	header := (*table.SDTHeader)(unsafe.Pointer(&stream[0]))
	header.Signature = [4]byte{'D', 'S', 'D', 'T'}
	header.Length = uint32(len(stream))
	header.Revision = 2

	return header
}

func pkgDir() string {
	_, f, _, _ := runtime.Caller(1)
	return filepath.Dir(f)
}

type mockResolver struct {
	pathToDumps string
	tableFiles  []string
}

func (m mockResolver) LookupTable(name string) *table.SDTHeader {
	for _, f := range m.tableFiles {
		if !strings.Contains(f, name) {
			continue
		}

		data, err := ioutil.ReadFile(filepath.Join(m.pathToDumps, f))
		if err != nil {
			panic(err)
		}

		return (*table.SDTHeader)(unsafe.Pointer(&data[0]))
	}

	return nil
}
