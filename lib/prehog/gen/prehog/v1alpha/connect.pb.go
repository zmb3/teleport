// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.28.1
// 	protoc        (unknown)
// source: prehog/v1alpha/connect.proto

package prehogv1alpha

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type ConnectUserLoginEvent struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// anonymized
	ClusterName string `protobuf:"bytes,1,opt,name=cluster_name,json=clusterName,proto3" json:"cluster_name,omitempty"`
	// anonymized
	UserName string `protobuf:"bytes,2,opt,name=user_name,json=userName,proto3" json:"user_name,omitempty"`
	// empty for local or github/saml/oidc
	ConnectorType string `protobuf:"bytes,3,opt,name=connector_type,json=connectorType,proto3" json:"connector_type,omitempty"`
}

func (x *ConnectUserLoginEvent) Reset() {
	*x = ConnectUserLoginEvent{}
	if protoimpl.UnsafeEnabled {
		mi := &file_prehog_v1alpha_connect_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ConnectUserLoginEvent) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConnectUserLoginEvent) ProtoMessage() {}

func (x *ConnectUserLoginEvent) ProtoReflect() protoreflect.Message {
	mi := &file_prehog_v1alpha_connect_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConnectUserLoginEvent.ProtoReflect.Descriptor instead.
func (*ConnectUserLoginEvent) Descriptor() ([]byte, []int) {
	return file_prehog_v1alpha_connect_proto_rawDescGZIP(), []int{0}
}

func (x *ConnectUserLoginEvent) GetClusterName() string {
	if x != nil {
		return x.ClusterName
	}
	return ""
}

func (x *ConnectUserLoginEvent) GetUserName() string {
	if x != nil {
		return x.UserName
	}
	return ""
}

func (x *ConnectUserLoginEvent) GetConnectorType() string {
	if x != nil {
		return x.ConnectorType
	}
	return ""
}

type Elooo = isConnectSubmitEventRequest_Event

type ConnectSubmitEventRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// anonymized
	DistinctId string `protobuf:"bytes,1,opt,name=distinct_id,json=distinctId,proto3" json:"distinct_id,omitempty"`
	// optional, will default to the ingest time if unset
	Timestamp *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=timestamp,proto3" json:"timestamp,omitempty"`
	// Types that are assignable to Event:
	//
	//	*ConnectSubmitEventRequest_UserLogin
	Event isConnectSubmitEventRequest_Event `protobuf_oneof:"event"`
}

func (x *ConnectSubmitEventRequest) Reset() {
	*x = ConnectSubmitEventRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_prehog_v1alpha_connect_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ConnectSubmitEventRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConnectSubmitEventRequest) ProtoMessage() {}

func (x *ConnectSubmitEventRequest) ProtoReflect() protoreflect.Message {
	mi := &file_prehog_v1alpha_connect_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConnectSubmitEventRequest.ProtoReflect.Descriptor instead.
func (*ConnectSubmitEventRequest) Descriptor() ([]byte, []int) {
	return file_prehog_v1alpha_connect_proto_rawDescGZIP(), []int{1}
}

func (x *ConnectSubmitEventRequest) GetDistinctId() string {
	if x != nil {
		return x.DistinctId
	}
	return ""
}

func (x *ConnectSubmitEventRequest) GetTimestamp() *timestamppb.Timestamp {
	if x != nil {
		return x.Timestamp
	}
	return nil
}

func (m *ConnectSubmitEventRequest) GetEvent() isConnectSubmitEventRequest_Event {
	if m != nil {
		return m.Event
	}
	return nil
}

func (x *ConnectSubmitEventRequest) GetUserLogin() *ConnectUserLoginEvent {
	if x, ok := x.GetEvent().(*ConnectSubmitEventRequest_UserLogin); ok {
		return x.UserLogin
	}
	return nil
}

type isConnectSubmitEventRequest_Event interface {
	isConnectSubmitEventRequest_Event()
}

type ConnectSubmitEventRequest_UserLogin struct {
	UserLogin *ConnectUserLoginEvent `protobuf:"bytes,3,opt,name=user_login,json=userLogin,proto3,oneof"`
}

func (*ConnectSubmitEventRequest_UserLogin) isConnectSubmitEventRequest_Event() {}

type ConnectSubmitEventResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *ConnectSubmitEventResponse) Reset() {
	*x = ConnectSubmitEventResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_prehog_v1alpha_connect_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ConnectSubmitEventResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConnectSubmitEventResponse) ProtoMessage() {}

func (x *ConnectSubmitEventResponse) ProtoReflect() protoreflect.Message {
	mi := &file_prehog_v1alpha_connect_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConnectSubmitEventResponse.ProtoReflect.Descriptor instead.
func (*ConnectSubmitEventResponse) Descriptor() ([]byte, []int) {
	return file_prehog_v1alpha_connect_proto_rawDescGZIP(), []int{2}
}

var File_prehog_v1alpha_connect_proto protoreflect.FileDescriptor

var file_prehog_v1alpha_connect_proto_rawDesc = []byte{
	0x0a, 0x1c, 0x70, 0x72, 0x65, 0x68, 0x6f, 0x67, 0x2f, 0x76, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61,
	0x2f, 0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x0e,
	0x70, 0x72, 0x65, 0x68, 0x6f, 0x67, 0x2e, 0x76, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0x1a, 0x1f,
	0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f,
	0x74, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22,
	0x7e, 0x0a, 0x15, 0x43, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x55, 0x73, 0x65, 0x72, 0x4c, 0x6f,
	0x67, 0x69, 0x6e, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x12, 0x21, 0x0a, 0x0c, 0x63, 0x6c, 0x75, 0x73,
	0x74, 0x65, 0x72, 0x5f, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0b,
	0x63, 0x6c, 0x75, 0x73, 0x74, 0x65, 0x72, 0x4e, 0x61, 0x6d, 0x65, 0x12, 0x1b, 0x0a, 0x09, 0x75,
	0x73, 0x65, 0x72, 0x5f, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08,
	0x75, 0x73, 0x65, 0x72, 0x4e, 0x61, 0x6d, 0x65, 0x12, 0x25, 0x0a, 0x0e, 0x63, 0x6f, 0x6e, 0x6e,
	0x65, 0x63, 0x74, 0x6f, 0x72, 0x5f, 0x74, 0x79, 0x70, 0x65, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x0d, 0x63, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x6f, 0x72, 0x54, 0x79, 0x70, 0x65, 0x22,
	0xc7, 0x01, 0x0a, 0x19, 0x43, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x53, 0x75, 0x62, 0x6d, 0x69,
	0x74, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x1f, 0x0a,
	0x0b, 0x64, 0x69, 0x73, 0x74, 0x69, 0x6e, 0x63, 0x74, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01,
	0x28, 0x09, 0x52, 0x0a, 0x64, 0x69, 0x73, 0x74, 0x69, 0x6e, 0x63, 0x74, 0x49, 0x64, 0x12, 0x38,
	0x0a, 0x09, 0x74, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x18, 0x02, 0x20, 0x01, 0x28,
	0x0b, 0x32, 0x1a, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x62, 0x75, 0x66, 0x2e, 0x54, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x52, 0x09, 0x74,
	0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x12, 0x46, 0x0a, 0x0a, 0x75, 0x73, 0x65, 0x72,
	0x5f, 0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x25, 0x2e, 0x70,
	0x72, 0x65, 0x68, 0x6f, 0x67, 0x2e, 0x76, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0x2e, 0x43, 0x6f,
	0x6e, 0x6e, 0x65, 0x63, 0x74, 0x55, 0x73, 0x65, 0x72, 0x4c, 0x6f, 0x67, 0x69, 0x6e, 0x45, 0x76,
	0x65, 0x6e, 0x74, 0x48, 0x00, 0x52, 0x09, 0x75, 0x73, 0x65, 0x72, 0x4c, 0x6f, 0x67, 0x69, 0x6e,
	0x42, 0x07, 0x0a, 0x05, 0x65, 0x76, 0x65, 0x6e, 0x74, 0x22, 0x1c, 0x0a, 0x1a, 0x43, 0x6f, 0x6e,
	0x6e, 0x65, 0x63, 0x74, 0x53, 0x75, 0x62, 0x6d, 0x69, 0x74, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x52,
	0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x32, 0x88, 0x01, 0x0a, 0x17, 0x43, 0x6f, 0x6e, 0x6e,
	0x65, 0x63, 0x74, 0x52, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x69, 0x6e, 0x67, 0x53, 0x65, 0x72, 0x76,
	0x69, 0x63, 0x65, 0x12, 0x6d, 0x0a, 0x12, 0x53, 0x75, 0x62, 0x6d, 0x69, 0x74, 0x43, 0x6f, 0x6e,
	0x6e, 0x65, 0x63, 0x74, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x12, 0x29, 0x2e, 0x70, 0x72, 0x65, 0x68,
	0x6f, 0x67, 0x2e, 0x76, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0x2e, 0x43, 0x6f, 0x6e, 0x6e, 0x65,
	0x63, 0x74, 0x53, 0x75, 0x62, 0x6d, 0x69, 0x74, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x52, 0x65, 0x71,
	0x75, 0x65, 0x73, 0x74, 0x1a, 0x2a, 0x2e, 0x70, 0x72, 0x65, 0x68, 0x6f, 0x67, 0x2e, 0x76, 0x31,
	0x61, 0x6c, 0x70, 0x68, 0x61, 0x2e, 0x43, 0x6f, 0x6e, 0x6e, 0x65, 0x63, 0x74, 0x53, 0x75, 0x62,
	0x6d, 0x69, 0x74, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65,
	0x22, 0x00, 0x42, 0xc3, 0x01, 0x0a, 0x12, 0x63, 0x6f, 0x6d, 0x2e, 0x70, 0x72, 0x65, 0x68, 0x6f,
	0x67, 0x2e, 0x76, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0x42, 0x0c, 0x43, 0x6f, 0x6e, 0x6e, 0x65,
	0x63, 0x74, 0x50, 0x72, 0x6f, 0x74, 0x6f, 0x50, 0x01, 0x5a, 0x46, 0x67, 0x69, 0x74, 0x68, 0x75,
	0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x67, 0x72, 0x61, 0x76, 0x69, 0x74, 0x61, 0x74, 0x69, 0x6f,
	0x6e, 0x61, 0x6c, 0x2f, 0x70, 0x72, 0x65, 0x68, 0x6f, 0x67, 0x2f, 0x67, 0x65, 0x6e, 0x2f, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x70, 0x72, 0x65, 0x68, 0x6f, 0x67, 0x2f, 0x76, 0x31, 0x61, 0x6c,
	0x70, 0x68, 0x61, 0x3b, 0x70, 0x72, 0x65, 0x68, 0x6f, 0x67, 0x76, 0x31, 0x61, 0x6c, 0x70, 0x68,
	0x61, 0xa2, 0x02, 0x03, 0x50, 0x58, 0x58, 0xaa, 0x02, 0x0e, 0x50, 0x72, 0x65, 0x68, 0x6f, 0x67,
	0x2e, 0x56, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0xca, 0x02, 0x0e, 0x50, 0x72, 0x65, 0x68, 0x6f,
	0x67, 0x5c, 0x56, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0xe2, 0x02, 0x1a, 0x50, 0x72, 0x65, 0x68,
	0x6f, 0x67, 0x5c, 0x56, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0x5c, 0x47, 0x50, 0x42, 0x4d, 0x65,
	0x74, 0x61, 0x64, 0x61, 0x74, 0x61, 0xea, 0x02, 0x0f, 0x50, 0x72, 0x65, 0x68, 0x6f, 0x67, 0x3a,
	0x3a, 0x56, 0x31, 0x61, 0x6c, 0x70, 0x68, 0x61, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_prehog_v1alpha_connect_proto_rawDescOnce sync.Once
	file_prehog_v1alpha_connect_proto_rawDescData = file_prehog_v1alpha_connect_proto_rawDesc
)

func file_prehog_v1alpha_connect_proto_rawDescGZIP() []byte {
	file_prehog_v1alpha_connect_proto_rawDescOnce.Do(func() {
		file_prehog_v1alpha_connect_proto_rawDescData = protoimpl.X.CompressGZIP(file_prehog_v1alpha_connect_proto_rawDescData)
	})
	return file_prehog_v1alpha_connect_proto_rawDescData
}

var file_prehog_v1alpha_connect_proto_msgTypes = make([]protoimpl.MessageInfo, 3)
var file_prehog_v1alpha_connect_proto_goTypes = []interface{}{
	(*ConnectUserLoginEvent)(nil),      // 0: prehog.v1alpha.ConnectUserLoginEvent
	(*ConnectSubmitEventRequest)(nil),  // 1: prehog.v1alpha.ConnectSubmitEventRequest
	(*ConnectSubmitEventResponse)(nil), // 2: prehog.v1alpha.ConnectSubmitEventResponse
	(*timestamppb.Timestamp)(nil),      // 3: google.protobuf.Timestamp
}
var file_prehog_v1alpha_connect_proto_depIdxs = []int32{
	3, // 0: prehog.v1alpha.ConnectSubmitEventRequest.timestamp:type_name -> google.protobuf.Timestamp
	0, // 1: prehog.v1alpha.ConnectSubmitEventRequest.user_login:type_name -> prehog.v1alpha.ConnectUserLoginEvent
	1, // 2: prehog.v1alpha.ConnectReportingService.SubmitConnectEvent:input_type -> prehog.v1alpha.ConnectSubmitEventRequest
	2, // 3: prehog.v1alpha.ConnectReportingService.SubmitConnectEvent:output_type -> prehog.v1alpha.ConnectSubmitEventResponse
	3, // [3:4] is the sub-list for method output_type
	2, // [2:3] is the sub-list for method input_type
	2, // [2:2] is the sub-list for extension type_name
	2, // [2:2] is the sub-list for extension extendee
	0, // [0:2] is the sub-list for field type_name
}

func init() { file_prehog_v1alpha_connect_proto_init() }
func file_prehog_v1alpha_connect_proto_init() {
	if File_prehog_v1alpha_connect_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_prehog_v1alpha_connect_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*ConnectUserLoginEvent); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_prehog_v1alpha_connect_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*ConnectSubmitEventRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_prehog_v1alpha_connect_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*ConnectSubmitEventResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	file_prehog_v1alpha_connect_proto_msgTypes[1].OneofWrappers = []interface{}{
		(*ConnectSubmitEventRequest_UserLogin)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_prehog_v1alpha_connect_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   3,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_prehog_v1alpha_connect_proto_goTypes,
		DependencyIndexes: file_prehog_v1alpha_connect_proto_depIdxs,
		MessageInfos:      file_prehog_v1alpha_connect_proto_msgTypes,
	}.Build()
	File_prehog_v1alpha_connect_proto = out.File
	file_prehog_v1alpha_connect_proto_rawDesc = nil
	file_prehog_v1alpha_connect_proto_goTypes = nil
	file_prehog_v1alpha_connect_proto_depIdxs = nil
}
