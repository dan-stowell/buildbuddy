package proto_test

import (
	"fmt"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/util/proto"
	"github.com/stretchr/testify/require"

	capb "github.com/buildbuddy-io/buildbuddy/proto/cache"
	dspb "github.com/buildbuddy-io/buildbuddy/proto/distributed_cache"
	sgpb "github.com/buildbuddy-io/buildbuddy/proto/storage"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/prototext"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	fileMetadataFixture = `
 file_record: {
   isolation: { cache_type: CAS remote_instance_name: "instance" partition_id: "partition" group_id: "group" }
   digest: { hash: "abc123" size_bytes: 123 }
   compressor: ZSTD encryption: { key_id: "key" } digest_function: SHA256
 }
 storage_metadata: {
   file_metadata: { filename: "cache/file" }
   inline_metadata: { data: "\000\001payload" created_at_nsec: 123456789 }
   gcs_metadata: { blob_name: "blob" last_custom_time_usec: 234567 shared: true }
 }
 encryption_metadata: { encryption_key_id: "encryption-key" version: 2 }
 stored_size_bytes: 456 last_access_usec: 345678 last_modify_usec: 456789
 `
	scoreCardResultFixture = `
 action_mnemonic: "GoCompile" target_id: "//foo:bar" action_id: "action"
 request_type: READ status: { code: 5 message: "missing" details: { type_url: "type.example/detail" value: "detail" } }
 digest: { hash: "def456" size_bytes: 789 }
 start_time: { seconds: 123 nanos: 456 } duration: { seconds: 2 nanos: 345 }
 compressor: ZSTD transferred_size_bytes: 678 name: "file.a" path_prefix: "bazel-out"
 cache_type: CAS execution_start_timestamp: { seconds: 100 nanos: 200 }
 execution_completed_timestamp: { seconds: 110 nanos: 300 } origin_invocation_id: "origin"
 `
	resourceFixture       = `digest: { hash: "abcdef" size_bytes: 321 } instance_name: "tree-instance" compressor: ZSTD cache_type: CAS digest_function: SHA256`
	nodePropertiesFixture = `properties: { name: "owner" value: "builder" } mtime: { seconds: 123 nanos: 456 } unix_mode: { value: 493 }`
	treeCacheFixture      = `
 children: {
   directory: {
     files: { name: "tool" digest: { hash: "123abc" size_bytes: 42 } is_executable: true node_properties: { ` + nodePropertiesFixture + ` } }
     directories: { name: "subdir" digest: { hash: "456def" size_bytes: 43 } }
     symlinks: { name: "link" target: "tool" node_properties: { ` + nodePropertiesFixture + ` } }
     node_properties: { ` + nodePropertiesFixture + ` }
   }
   resource_name: { ` + resourceFixture + ` }
 }
 tree_cache_children: { ` + resourceFixture + ` }
 `
)

var (
	benchmarkMarshalResult []byte

	testProtoTypes = map[string]testProtoType{
		"FileMetadata": testProtoType{
			fixture: fileMetadataFixture,
			providerFn: func() protoMessage {
				return &sgpb.FileMetadata{}
			},
		},
		"ScoreCard": testProtoType{
			fixture: `misses: { ` + scoreCardResultFixture + ` } results: { ` + scoreCardResultFixture + ` }`,
			providerFn: func() protoMessage {
				return &capb.ScoreCard{}
			},
		},
		"ScoreCardResult": testProtoType{
			fixture: scoreCardResultFixture,
			providerFn: func() protoMessage {
				return &capb.ScoreCard_Result{}
			},
		},
		"TreeCache": testProtoType{
			fixture: treeCacheFixture,
			providerFn: func() protoMessage {
				return &capb.TreeCache{}
			},
		},
		"ReadResponse": testProtoType{
			fixture: `data: "payload" reference: { metadata: { ` + fileMetadataFixture + ` } }`,
			providerFn: func() protoMessage {
				return &dspb.ReadResponse{}
			},
		},
	}
)

type protoMessage interface {
	SizeVT() int
	proto.Message
}

type protoMessageWithPoolEnabled interface {
	protoMessage
	ReturnToVTPool()
}

type providerFunc func() protoMessage
type providerWithPoolFunc func() protoMessageWithPoolEnabled

type testProtoType struct {
	fixture            string
	providerFn         providerFunc
	providerFnWithPool providerWithPoolFunc
}

// Require every field to be populated, including nested messages, so new proto
// fields require an explicit fixture update instead of silently losing coverage.
func requireAllFieldsPopulated(t testing.TB, msg protoreflect.Message) {
	t.Helper()
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		require.True(t, msg.Has(field), "fixture omits %s", field.FullName())
		if field.Message() == nil {
			continue
		}
		if field.IsList() {
			list := msg.Get(field).List()
			for j := 0; j < list.Len(); j++ {
				requireAllFieldsPopulated(t, list.Get(j).Message())
			}
		} else {
			requireAllFieldsPopulated(t, msg.Get(field).Message())
		}
	}
}

func generateProtos(t testing.TB, pbType testProtoType) []protoMessage {
	t.Helper()
	populated := pbType.providerFn()
	require.NoError(t, prototext.Unmarshal([]byte(pbType.fixture), populated))
	requireAllFieldsPopulated(t, populated.ProtoReflect())
	// Exercise both empty/default fields and fully populated messages.
	return []protoMessage{pbType.providerFn(), populated}
}

func fileMetadata(t testing.TB) *sgpb.FileMetadata {
	t.Helper()
	return generateProtos(t, testProtoTypes["FileMetadata"])[1].(*sgpb.FileMetadata)
}

func generateBytes(t testing.TB, protos []protoMessage) [][]byte {
	res := make([][]byte, 0, len(protos))
	for _, pb := range protos {
		buf, err := gproto.Marshal(pb)
		require.NoError(t, err, "unable to marshal")
		res = append(res, buf)
	}
	return res
}

type marshalFunc func(v protoMessage) ([]byte, error)
type unmarshalFunc func([]byte, protoMessage) error
type cloneFunc func(v proto.Message) proto.Message
type sizeFunc func(v proto.Message) int

func TestMarshal(t *testing.T) {
	md := fileMetadata(t)

	actual, err := proto.Marshal(md)
	require.NoError(t, err)
	expected, err := gproto.Marshal(md)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func TestMarshalFallback(t *testing.T) {
	msg := wrapperspb.String("fallback")
	expected, err := gproto.Marshal(msg)
	require.NoError(t, err)
	actual, err := proto.Marshal(msg)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func TestMarshalWithExternalVTMessage(t *testing.T) {
	detail := &anypb.Any{}
	// Field 1 is type_url; field 100 is an unknown varint field.
	require.NoError(t, gproto.Unmarshal([]byte{0x0a, 0x01, 'x', 0xa0, 0x06, 0x01}, detail))

	status := &statuspb.Status{
		Code:    13,
		Message: "internal error",
		Details: []*anypb.Any{detail},
	}
	scoreCard := &capb.ScoreCard{
		Results: []*capb.ScoreCard_Result{{
			ActionMnemonic: "CppCompile",
			Status:         status,
		}},
	}
	_, ok := any(scoreCard.Results[0].Status).(interface {
		MarshalToSizedBufferVT([]byte) (int, error)
	})
	require.True(t, ok)

	expected, err := gproto.Marshal(scoreCard)
	require.NoError(t, err)
	actual, err := proto.Marshal(scoreCard)
	require.NoError(t, err)
	require.Equal(t, expected, actual)

	cloned := status.CloneVT()
	require.True(t, gproto.Equal(status, cloned))
	statusBytes, err := gproto.Marshal(status)
	require.NoError(t, err)
	decoded := &statuspb.Status{}
	require.NoError(t, decoded.UnmarshalVT(statusBytes))
	require.True(t, gproto.Equal(status, decoded))
}

func TestUnmarshal(t *testing.T) {
	md := fileMetadata(t)

	data, err := gproto.Marshal(md)
	require.NoError(t, err)

	actual := &sgpb.FileMetadata{}
	err = proto.Unmarshal(data, actual)
	require.NoError(t, err)

	require.True(t, proto.Equal(md, actual))
}

func TestClone(t *testing.T) {
	md := fileMetadata(t)

	actual := proto.Clone(md)
	require.True(t, proto.Equal(md, actual))
}

func TestSizeVT(t *testing.T) {
	for name, pbType := range testProtoTypes {
		t.Run(name, func(t *testing.T) {
			for _, msg := range generateProtos(t, pbType) {
				require.Implements(t, (*proto.VTProtoMessage)(nil), msg)
				require.Equal(t, gproto.Size(msg), proto.Size(msg))
			}
		})
	}

	t.Run("typed nil", func(t *testing.T) {
		msg := (*sgpb.FileMetadata)(nil)
		require.Implements(t, (*proto.VTProtoMessage)(nil), msg)
		require.Equal(t, gproto.Size(msg), proto.Size(msg))
	})

	t.Run("unknown fields", func(t *testing.T) {
		msg := &sgpb.FileMetadata{}
		msg.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		require.Implements(t, (*proto.VTProtoMessage)(nil), msg)
		require.Equal(t, gproto.Size(msg), proto.Size(msg))
	})
}

func benchmarkMarshal(b *testing.B, marshalFn marshalFunc, data []protoMessage) {
	b.ReportAllocs()
	var totalSize int
	for _, pb := range data {
		totalSize += pb.SizeVT()
	}
	b.SetBytes(int64((totalSize + len(data)/2) / len(data)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pb := data[i%len(data)]
		buf, err := marshalFn(pb)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkMarshalResult = buf
	}

}

func benchmarkUnmarshal(b *testing.B, unmarshalFn unmarshalFunc, providerFn providerFunc, data [][]byte) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf := data[i%len(data)]
		b.SetBytes(int64(len(buf)))
		v := providerFn()
		err := unmarshalFn(buf, v)
		if err != nil {
			b.Fatal(err)
		}
	}

}

func BenchmarkMarshal(b *testing.B) {
	marshalFns := map[string]marshalFunc{
		"Old": func(v protoMessage) ([]byte, error) {
			return gproto.Marshal(v)
		},
		"New": func(v protoMessage) ([]byte, error) {
			return proto.Marshal(v)
		},
	}

	for pbName, pbType := range testProtoTypes {
		protos := generateProtos(b, pbType)
		for name, fn := range marshalFns {
			b.Run(fmt.Sprintf("name=%s/pbName=%s", name, pbName), func(b *testing.B) {
				benchmarkMarshal(b, fn, protos)
			})
		}
	}
}

func BenchmarkUnmarshal(b *testing.B) {
	unmarshalFns := map[string]unmarshalFunc{
		"New": func(buf []byte, v protoMessage) error {
			return proto.Unmarshal(buf, v)
		},
		"Old": func(buf []byte, v protoMessage) error {
			return gproto.Unmarshal(buf, v)
		},
	}

	for pbName, pbType := range testProtoTypes {
		protos := generateProtos(b, pbType)
		data := generateBytes(b, protos)
		for name, fn := range unmarshalFns {
			b.Run(fmt.Sprintf("name=%s/pbName=%s", name, pbName), func(b *testing.B) {
				benchmarkUnmarshal(b, fn, pbType.providerFn, data)
			})

		}

		if pbType.providerFnWithPool != nil {
			b.Run(fmt.Sprintf("name=WithPool/pbName=%s", pbName), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					buf := data[i%len(data)]
					b.SetBytes(int64(len(buf)))
					v := pbType.providerFnWithPool()
					err := proto.Unmarshal(buf, v)
					if err != nil {
						b.Fatal(err)
					}
					v.ReturnToVTPool()
				}
			})
		}
	}
}

func benchmarkClone(b *testing.B, cloneFn cloneFunc, data []protoMessage) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pb := data[i%len(data)]
		b.SetBytes(int64(pb.SizeVT()))
		_ = cloneFn(pb)
	}

}

func BenchmarkClone(b *testing.B) {
	cloneFns := map[string]cloneFunc{
		"New": func(v proto.Message) proto.Message {
			return proto.Clone(v)
		},
		"Old": func(v proto.Message) proto.Message {
			return gproto.Clone(v)
		},
	}

	for pbName, pbType := range testProtoTypes {
		protos := generateProtos(b, pbType)
		for name, fn := range cloneFns {
			b.Run(fmt.Sprintf("name=%s/pbName=%s", name, pbName), func(b *testing.B) {
				benchmarkClone(b, fn, protos)
			})
		}
	}
}

func BenchmarkSize(b *testing.B) {
	sizeFns := map[string]sizeFunc{
		"New": func(v proto.Message) int {
			return proto.Size(v)
		},
		"Old": func(v proto.Message) int {
			return gproto.Size(v)
		},
	}

	for pbName, pbType := range testProtoTypes {
		protos := generateProtos(b, pbType)
		for name, fn := range sizeFns {
			b.Run(fmt.Sprintf("name=%s/pbName=%s", name, pbName), func(b *testing.B) {
				b.ReportAllocs()
				var totalSize int
				for _, pb := range protos {
					totalSize += gproto.Size(pb)
				}
				b.SetBytes(int64(totalSize / len(protos)))
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					pb := protos[i%len(protos)]
					_ = fn(pb)
				}
			})
		}
	}
}
