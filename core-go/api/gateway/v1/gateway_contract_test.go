package proto

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestAiLogicServiceContract(t *testing.T) {
	services := File_gateway_proto.Services()
	if services.Len() != 1 {
		t.Fatalf("expected exactly one service in gateway proto, got %d", services.Len())
	}

	svc := services.ByName("AiLogic")
	if svc == nil {
		t.Fatal("expected AiLogic service descriptor")
	}

	expectedMethods := []protoreflect.Name{"CheckInput", "CheckOutput", "GetCache", "CountTokens"}
	if svc.Methods().Len() != len(expectedMethods) {
		t.Fatalf("expected %d AiLogic methods, got %d", len(expectedMethods), svc.Methods().Len())
	}

	for _, methodName := range expectedMethods {
		method := svc.Methods().ByName(methodName)
		if method == nil {
			t.Fatalf("expected method %s to exist", methodName)
		}
	}

	assertMethodSignature(t, svc.Methods().ByName("CheckInput"), "InputRequest", "InputResponse")
	assertMethodSignature(t, svc.Methods().ByName("CheckOutput"), "OutputRequest", "OutputResponse")
	assertMethodSignature(t, svc.Methods().ByName("GetCache"), "CacheRequest", "CacheResponse")
	assertMethodSignature(t, svc.Methods().ByName("CountTokens"), "TokenRequest", "TokenResponse")
}

func TestGatewayMessageFieldNumbersRemainStable(t *testing.T) {
	messages := File_gateway_proto.Messages()

	assertFieldNumber(t, messages.ByName("InputRequest"), "prompt", 1)
	assertFieldNumber(t, messages.ByName("InputRequest"), "metadata", 2)

	assertFieldNumber(t, messages.ByName("InputResponse"), "safe", 1)
	assertFieldNumber(t, messages.ByName("InputResponse"), "sanitized_prompt", 2)
	assertFieldNumber(t, messages.ByName("InputResponse"), "reason", 3)

	assertFieldNumber(t, messages.ByName("OutputRequest"), "response_text", 1)
	assertFieldNumber(t, messages.ByName("OutputResponse"), "safe", 1)
	assertFieldNumber(t, messages.ByName("OutputResponse"), "sanitized_text", 2)

	assertFieldNumber(t, messages.ByName("CacheRequest"), "prompt", 1)
	assertFieldNumber(t, messages.ByName("CacheRequest"), "model", 2)
	assertFieldNumber(t, messages.ByName("CacheResponse"), "hit", 1)
	assertFieldNumber(t, messages.ByName("CacheResponse"), "response", 2)

	assertFieldNumber(t, messages.ByName("TokenRequest"), "text", 1)
	assertFieldNumber(t, messages.ByName("TokenRequest"), "model", 2)
	assertFieldNumber(t, messages.ByName("TokenResponse"), "count", 1)
}

func assertMethodSignature(t *testing.T, method protoreflect.MethodDescriptor, inputName, outputName protoreflect.Name) {
	t.Helper()
	if method == nil {
		t.Fatal("method descriptor must not be nil")
	}
	if method.Input().Name() != inputName {
		t.Fatalf("expected %s input %s, got %s", method.Name(), inputName, method.Input().Name())
	}
	if method.Output().Name() != outputName {
		t.Fatalf("expected %s output %s, got %s", method.Name(), outputName, method.Output().Name())
	}
}

func assertFieldNumber(t *testing.T, message protoreflect.MessageDescriptor, fieldName protoreflect.Name, number protoreflect.FieldNumber) {
	t.Helper()
	if message == nil {
		t.Fatal("message descriptor must not be nil")
	}

	field := message.Fields().ByName(fieldName)
	if field == nil {
		t.Fatalf("expected field %s on message %s", fieldName, message.Name())
	}
	if field.Number() != number {
		t.Fatalf("expected %s.%s to use field number %d, got %d", message.Name(), fieldName, number, field.Number())
	}
}
