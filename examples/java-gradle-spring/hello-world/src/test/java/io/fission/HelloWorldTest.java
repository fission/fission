package io.fission;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.Objects;

import org.springframework.http.*;
import org.springframework.util.Assert;

public class HelloWorldTest {
	public void testResponse() {
		HelloWorld hw = new HelloWorld();
		RequestEntity request = null;
		try {
			request = RequestEntity.get(new URI("http://baidu.com")).build();
		} catch (URISyntaxException e) {
			e.printStackTrace();
		}
		ResponseEntity resp = hw.call(request,null);
		Assert.hasText(Objects.requireNonNull(resp.getBody()).toString(), "Hello World!");
	}
}
