package io.harnessgateway;

import java.util.concurrent.atomic.AtomicBoolean;

import okhttp3.MediaType;
import okhttp3.ResponseBody;
import okio.BufferedSource;

import org.springframework.ai.openai.http.okhttp.OpenAiHttpClientBuilderCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/** Ensure cancelling an idle SSE stream aborts its HTTP call immediately. */
@Configuration(proxyBeanMethods = false)
public class GatewayHttpConfiguration {

    @Bean
    OpenAiHttpClientBuilderCustomizer cancelStreamingCallOnClose() {
        return builder -> builder.interceptor(chain -> {
            var response = chain.proceed(chain.request());
            var body = response.body();
            var type = body == null ? null : body.contentType();
            if (type == null || !"text".equals(type.type()) || !"event-stream".equals(type.subtype())) {
                return response;
            }
            return response.newBuilder().body(new ResponseBody() {
                private final AtomicBoolean closed = new AtomicBoolean();

                @Override
                public MediaType contentType() { return body.contentType(); }

                @Override
                public long contentLength() { return body.contentLength(); }

                @Override
                public BufferedSource source() { return body.source(); }

                @Override
                public void close() {
                    if (closed.compareAndSet(false, true)) {
                        // Spring AI 2.0.2 closes the response body without cancelling
                        // the call. Abort the socket before a concurrent read can
                        // hold body.close() until the next chunk or request timeout.
                        chain.call().cancel();
                        body.close();
                    }
                }
            }).build();
        });
    }
}
