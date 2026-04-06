package main

import (
    "encoding/base64"
    "fmt"
    "log"

    gatewayproto "github.com/ai-gateway/core/api/gateway/v1"
    "google.golang.org/protobuf/proto"
    "google.golang.org/protobuf/reflect/protodesc"
)

func main() {
    fdProto := protodesc.ToFileDescriptorProto(gatewayproto.File_gateway_proto)
    data, err := proto.MarshalOptions{Deterministic: true}.Marshal(fdProto)
    if err != nil {
        log.Fatalf("marshal descriptor: %v", err)
    }
    fmt.Print(base64.StdEncoding.EncodeToString(data))
}