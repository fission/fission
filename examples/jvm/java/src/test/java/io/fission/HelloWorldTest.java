package io.fission;

import java.net.URI;
import java.net.URISyntaxException;

import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.http.ResponseEntity;
import org.springframework.util.Assert;

public class HelloWorldTest {

	public void testResponse() {
		HelloWorld hw = new HelloWorld();
		RequestEntity request = null;
		try {
			request = RequestEntity.get(new URI("http://example.com/bar")).build();
		} catch (URISyntaxException e) {
			e.printStackTrace();
		}
		ResponseEntity resp = hw.call(request, null);
		Assert.hasText(resp.getBody().toString(), "Hello World!");
	}
}