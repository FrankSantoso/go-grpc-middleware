package grpc_zerolog_test

import (
	grpc_zerolog "github.com/grpc-ecosystem/go-grpc-middleware/logging/zerolog"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	pb_testproto "github.com/grpc-ecosystem/go-grpc-middleware/testing/testproto"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

func customCodeToLevel(c codes.Code) zerolog.Level {
	if c == codes.Unauthenticated {
		// Make this a special case for tests, and an error.
		return zerolog.ErrorLevel
	}
	return grpc_zerolog.DefaultCodeToLevel(c)
}

func TestZRLoggingSuite(t *testing.T) {
	if strings.HasPrefix(runtime.Version(), "go1.7") {
		t.Skipf("Skipping due to json.RawMessage incompatibility with go1.7")
		return
	}
	opts := []grpc_zerolog.Option{
		grpc_zerolog.WithLevels(customCodeToLevel),
	}
	b := newZRBaseSuite(t)
	b.InterceptorTestSuite.ServerOpts = []grpc.ServerOption{
		grpc_middleware.WithStreamServerChain(
			grpc_ctxtags.StreamServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
			grpc_zerolog.StreamServerInterceptor(b.logger.Logger, opts...)),
		grpc_middleware.WithUnaryServerChain(
			grpc_ctxtags.UnaryServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
			grpc_zerolog.UnaryServerInterceptor(b.logger.Logger, opts...)),
	}
	suite.Run(t, &ZRServerSuite{b})
}

type ZRServerSuite struct {
	*ZRBaseSuite
}

func (s *ZRServerSuite) TestPing_WithCustomTags() {
	deadline := time.Now().Add(3 * time.Second)
	_, err := s.Client.Ping(s.DeadlineCtx(deadline), goodPing)
	require.NoError(s.T(), err, "there must be not be an error on a successful call")

	msgs := s.getOutputJSONs()
	require.Len(s.T(), msgs, 2, "two log statements should be logged")
	for _, m := range msgs {
		assert.Equal(s.T(), m["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
		assert.Equal(s.T(), m["grpc.method"], "Ping", "all lines must contain method name")
		assert.Equal(s.T(), m["span.kind"], "server", "all lines must contain the kind of call (server)")
		assert.Equal(s.T(), m["custom_tags.string"], "something", "all lines must contain `custom_tags.string`")
		assert.Equal(s.T(), m["grpc.request.value"], "something", "all lines must contain fields extracted")
		assert.Equal(s.T(), m["custom_field"], "custom_value", "all lines must contain `custom_field`")

		assert.Contains(s.T(), m, "custom_tags.int", "all lines must contain `custom_tags.int`")
		require.Contains(s.T(), m, "grpc.start_time", "all lines must contain the start time")
		_, err := time.Parse(time.RFC3339, m["grpc.start_time"].(string))
		assert.NoError(s.T(), err, "should be able to parse start time as RFC3339")

		require.Contains(s.T(), m, "grpc.request.deadline", "all lines must contain the deadline of the call")
		_, err = time.Parse(time.RFC3339, m["grpc.request.deadline"].(string))
		require.NoError(s.T(), err, "should be able to parse deadline as RFC3339")
		assert.Equal(s.T(), m["grpc.request.deadline"], deadline.Format(time.RFC3339), "should have the same deadline that was set by the caller")
	}

	assert.Equal(s.T(), msgs[0]["msg"], "some ping", "handler's message must contain user message")

	assert.Equal(s.T(), msgs[1]["msg"], "finished unary call with code OK", "handler's message must contain user message")
	assert.Equal(s.T(), msgs[1]["level"], "info", "must be logged at info level")
	assert.Contains(s.T(), msgs[1], "grpc.time_ms", "interceptor log statement should contain execution time")
}

func (s *ZRServerSuite) TestPingError_WithCustomLevels() {
	for _, tcase := range []struct {
		code  codes.Code
		level zerolog.Level
		msg   string
	}{
		{
			code:  codes.Internal,
			level: zerolog.ErrorLevel,
			msg:   "Internal must remap to ErrorLevel in DefaultCodeToLevel",
		},
		{
			code:  codes.NotFound,
			level: zerolog.InfoLevel,
			msg:   "NotFound must remap to InfoLevel in DefaultCodeToLevel",
		},
		{
			code:  codes.FailedPrecondition,
			level: zerolog.WarnLevel,
			msg:   "FailedPrecondition must remap to WarnLevel in DefaultCodeToLevel",
		},
		{
			code:  codes.Unauthenticated,
			level: zerolog.ErrorLevel,
			msg:   "Unauthenticated is overwritten to PanicLevel with customCodeToLevel override, which probably didn't work",
		},
	} {
		s.buffer.Reset()
		_, err := s.Client.PingError(
			s.SimpleCtx(),
			&pb_testproto.PingRequest{Value: "something", ErrorCodeReturned: uint32(tcase.code)})
		require.Error(s.T(), err, "each call here must return an error")

		msgs := s.getOutputJSONs()
		require.Len(s.T(), msgs, 1, "only the interceptor log message is printed in PingErr")

		m := msgs[0]
		assert.Equal(s.T(), m["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
		assert.Equal(s.T(), m["grpc.method"], "PingError", "all lines must contain method name")
		assert.Equal(s.T(), m["grpc.code"], tcase.code.String(), "all lines have the correct gRPC code")
		assert.Equal(s.T(), m["level"], tcase.level.String(), tcase.msg)
		assert.Equal(s.T(), m["msg"], "finished unary call with code "+tcase.code.String(), "needs the correct end message")

		require.Contains(s.T(), m, "grpc.start_time", "all lines must contain the start time")
		_, err = time.Parse(time.RFC3339, m["grpc.start_time"].(string))
		assert.NoError(s.T(), err, "should be able to parse start time as RFC3339")
	}
}

func (s *ZRServerSuite) TestPingList_WithCustomTags() {
	stream, err := s.Client.PingList(s.SimpleCtx(), goodPing)
	require.NoError(s.T(), err, "should not fail on establishing the stream")
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(s.T(), err, "reading stream should not fail")
	}
	msgs := s.getOutputJSONs()
	require.Len(s.T(), msgs, 2, "two log statements should be logged")

	for _, m := range msgs {
		assert.Equal(s.T(), m["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
		assert.Equal(s.T(), m["grpc.method"], "PingList", "all lines must contain method name")
		assert.Equal(s.T(), m["span.kind"], "server", "all lines must contain the kind of call (server)")
		assert.Equal(s.T(), m["custom_tags.string"], "something", "all lines must contain `custom_tags.string` set by AddFields")
		assert.Equal(s.T(), m["grpc.request.value"], "something", "all lines must contain fields extracted from goodPing because of test.manual_extractfields.pb")

		assert.Contains(s.T(), m, "custom_tags.int", "all lines must contain `custom_tags.int` set by AddFields")
		require.Contains(s.T(), m, "grpc.start_time", "all lines must contain the start time")
		_, err := time.Parse(time.RFC3339, m["grpc.start_time"].(string))
		assert.NoError(s.T(), err, "should be able to parse start time as RFC3339")
	}

	assert.Equal(s.T(), msgs[0]["msg"], "some pinglist", "handler's message must contain user message")

	assert.Equal(s.T(), msgs[1]["msg"], "finished streaming call with code OK", "handler's message must contain user message")
	assert.Equal(s.T(), msgs[1]["level"], "info", "OK codes must be logged on info level.")
	assert.Contains(s.T(), msgs[1], "grpc.time_ms", "interceptor log statement should contain execution time")
}

func TestZRLoggingOverrideSuite(t *testing.T) {
	if strings.HasPrefix(runtime.Version(), "go1.7") {
		t.Skip("Skipping due to json.RawMessage incompatibility with go1.7")
		return
	}
	opts := []grpc_zerolog.Option{
		grpc_zerolog.WithDurationField(grpc_zerolog.DurationToDurationField),
	}
	b := newZRBaseSuite(t)
	b.InterceptorTestSuite.ServerOpts = []grpc.ServerOption{
		grpc_middleware.WithStreamServerChain(
			grpc_ctxtags.StreamServerInterceptor(),
			grpc_zerolog.StreamServerInterceptor(b.logger.Logger, opts...)),
		grpc_middleware.WithUnaryServerChain(
			grpc_ctxtags.UnaryServerInterceptor(),
			grpc_zerolog.UnaryServerInterceptor(b.logger.Logger, opts...)),
	}
	suite.Run(t, &ZRServerOverrideSuite{b})
}

type ZRServerOverrideSuite struct {
	*ZRBaseSuite
}

func (s *ZRServerOverrideSuite) TestPing_HasOverriddenDuration() {
	_, err := s.Client.Ping(s.SimpleCtx(), goodPing)
	require.NoError(s.T(), err, "there must be not be an error on a successful call")
	msgs := s.getOutputJSONs()
	require.Len(s.T(), msgs, 2, "two log statements should be logged")

	for _, m := range msgs {
		assert.Equal(s.T(), m["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
		assert.Equal(s.T(), m["grpc.method"], "Ping", "all lines must contain method name")
	}
	assert.Equal(s.T(), msgs[0]["msg"], "some ping", "handler's message must contain user message")
	assert.NotContains(s.T(), msgs[0], "grpc.time_ms", "handler's message must not contain default duration")
	assert.NotContains(s.T(), msgs[0], "grpc.duration", "handler's message must not contain overridden duration")

	assert.Equal(s.T(), msgs[1]["msg"], "finished unary call with code OK", "handler's message must contain user message")
	assert.Equal(s.T(), msgs[1]["level"], "info", "OK error codes must be logged on info level.")
	assert.NotContains(s.T(), msgs[1], "grpc.time_ms", "handler's message must not contain default duration")
	assert.Contains(s.T(), msgs[1], "grpc.duration", "handler's message must contain overridden duration")
}

func (s *ZRServerOverrideSuite) TestPingList_HasOverriddenDuration() {
	stream, err := s.Client.PingList(s.SimpleCtx(), goodPing)
	require.NoError(s.T(), err, "should not fail on establishing the stream")
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(s.T(), err, "reading stream should not fail")
	}
	msgs := s.getOutputJSONs()
	require.Len(s.T(), msgs, 2, "two log statements should be logged")
	for _, m := range msgs {
		s.T()
		assert.Equal(s.T(), m["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
		assert.Equal(s.T(), m["grpc.method"], "PingList", "all lines must contain method name")
	}

	assert.Equal(s.T(), msgs[0]["msg"], "some pinglist", "handler's message must contain user message")
	assert.NotContains(s.T(), msgs[0], "grpc.time_ms", "handler's message must not contain default duration")
	assert.NotContains(s.T(), msgs[0], "grpc.duration", "handler's message must not contain overridden duration")

	assert.Equal(s.T(), msgs[1]["msg"], "finished streaming call with code OK", "handler's message must contain user message")
	assert.Equal(s.T(), msgs[1]["level"], "info", "OK error codes must be logged on info level.")
	assert.NotContains(s.T(), msgs[1], "grpc.time_ms", "handler's message must not contain default duration")
	assert.Contains(s.T(), msgs[1], "grpc.duration", "handler's message must contain overridden duration")
}

func TestZRServerOverrideSuppressedSuite(t *testing.T) {
	if strings.HasPrefix(runtime.Version(), "go1.7") {
		t.Skip("Skipping due to json.RawMessage incompatibility with go1.7")
		return
	}
	opts := []grpc_zerolog.Option{
		grpc_zerolog.WithDecider(func(method string, err error) bool {
			if err != nil && method == "/mwitkow.testproto.TestService/PingError" {
				return true
			}
			return false
		}),
	}
	b := newZRBaseSuite(t)
	b.InterceptorTestSuite.ServerOpts = []grpc.ServerOption{
		grpc_middleware.WithStreamServerChain(
			grpc_ctxtags.StreamServerInterceptor(),
			grpc_zerolog.StreamServerInterceptor(b.logger.Logger, opts...)),
		grpc_middleware.WithUnaryServerChain(
			grpc_ctxtags.UnaryServerInterceptor(),
			grpc_zerolog.UnaryServerInterceptor(b.logger.Logger, opts...)),
	}
	suite.Run(t, &ZRServerOverridenDeciderSuite{b})
}

type ZRServerOverridenDeciderSuite struct {
	*ZRBaseSuite
}

func (s *ZRServerOverridenDeciderSuite) TestPing_HasOverriddenDecider() {
	_, err := s.Client.Ping(s.SimpleCtx(), goodPing)
	require.NoError(s.T(), err, "there must be not be an error on a successful call")
	msgs := s.getOutputJSONs()
	require.Len(s.T(), msgs, 1, "single log statements should be logged")

	assert.Equal(s.T(), msgs[0]["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
	assert.Equal(s.T(), msgs[0]["grpc.method"], "Ping", "all lines must contain method name")
	assert.Equal(s.T(), msgs[0]["msg"], "some ping", "handler's message must contain user message")
}

func (s *ZRServerOverridenDeciderSuite) TestPingError_HasOverriddenDecider() {
	code := codes.NotFound
	level := zerolog.InfoLevel
	msg := "NotFound must remap to InfoLevel in DefaultCodeToLevel"

	s.buffer.Reset()
	_, err := s.Client.PingError(
		s.SimpleCtx(),
		&pb_testproto.PingRequest{Value: "something", ErrorCodeReturned: uint32(code)})
	require.Error(s.T(), err, "each call here must return an error")
	msgs := s.getOutputJSONs()
	require.Len(s.T(), msgs, 1, "only the interceptor log message is printed in PingErr")
	m := msgs[0]
	assert.Equal(s.T(), m["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
	assert.Equal(s.T(), m["grpc.method"], "PingError", "all lines must contain method name")
	assert.Equal(s.T(), m["grpc.code"], code.String(), "all lines must contain the correct gRPC code")
	assert.Equal(s.T(), m["level"], level.String(), msg)
}

func (s *ZRServerOverridenDeciderSuite) TestPingList_HasOverriddenDecider() {
	stream, err := s.Client.PingList(s.SimpleCtx(), goodPing)
	require.NoError(s.T(), err, "should not fail on establishing the stream")
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(s.T(), err, "reading stream should not fail")
	}
	msgs := s.getOutputJSONs()
	require.Len(s.T(), msgs, 1, "single log statements should be logged")

	assert.Equal(s.T(), msgs[0]["grpc.service"], "mwitkow.testproto.TestService", "all lines must contain service name")
	assert.Equal(s.T(), msgs[0]["grpc.method"], "PingList", "all lines must contain method name")
	assert.Equal(s.T(), msgs[0]["msg"], "some pinglist", "handler's message must contain user message")

	assert.NotContains(s.T(), msgs[0], "grpc.time_ms", "handler's message must not contain default duration")
	assert.NotContains(s.T(), msgs[0], "grpc.duration", "handler's message must not contain overridden duration")
}
