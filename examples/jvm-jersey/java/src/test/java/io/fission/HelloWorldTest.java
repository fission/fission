package io.fission;

import java.net.URI;
import java.net.URISyntaxException;
import org.glassfish.jersey.server.ContainerRequest;
import javax.ws.rs.core.Response;
import org.junit.Assert;
import org.junit.Test;


public class HelloWorldTest {

	@Test
	public void testResponse() {
		HelloWorld hw = new HelloWorld();
		ContainerRequest request = null;
		try {
			request = new ContainerRequest(new URI("http://example.com/"),new URI("/hello"),"GET",null,null);
		} catch (URISyntaxException e) {
			e.printStackTrace();
		}
		Response response = hw.call(request, null);
		Assert.assertTrue(response.getEntity().toString().equals(HelloWorld.RETURN_STRING));
	}
}
