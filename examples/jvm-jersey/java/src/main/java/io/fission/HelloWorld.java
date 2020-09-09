package io.fission;

import javax.ws.rs.core.Response;
import javax.ws.rs.container.ContainerRequestContext;
import io.fission.Function;
import java.io.BufferedReader;
import java.util.stream.Collectors;
import java.io.InputStreamReader;

public class HelloWorld implements Function<ContainerRequestContext,Response> {

	public static final String RETURN_STRING = "Hello World!";

	@Override
	public Response call(ContainerRequestContext request, Context arg1) {		
		if(request.getMethod().equals("GET")) {
			return Response.ok(RETURN_STRING).build();
		}
		else {
			String body = new BufferedReader(new InputStreamReader(request.getEntityStream())).lines()
			.parallel().collect(Collectors.joining("\n"));
			return Response.ok("Echo: " + body).build();
		}
	}
}
