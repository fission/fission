package io.fission;

import org.eclipse.jetty.server.Server;
import org.eclipse.jetty.servlet.ServletContextHandler;
import org.eclipse.jetty.servlet.ServletHolder;
import org.glassfish.jersey.jackson.JacksonFeature;
import org.glassfish.jersey.server.ResourceConfig;
import org.glassfish.jersey.servlet.ServletContainer;

public class Main {

	private static final int DEFAULT_PORT = 8888;
	
	private int serverPort;
	
	public Main(int serverPort) throws Exception {
		this.serverPort = serverPort;
		Server server = configureServer();	        
        server.start();
        server.join();
	}	

	private Server configureServer() {
		ResourceConfig resourceConfig = new ResourceConfig();		
		resourceConfig.packages(JerseyServer.class.getPackage().getName());
		resourceConfig.register(JacksonFeature.class);
		ServletContainer servletContainer = new ServletContainer(resourceConfig);
		ServletHolder sh = new ServletHolder(servletContainer);
		Server server = new Server(serverPort);		
		ServletContextHandler context = new ServletContextHandler(ServletContextHandler.SESSIONS);
        context.setContextPath("/");
        context.addServlet(sh, "/*");
		server.setHandler(context);
		return server;
	}
	
	public static void main(String[] args) throws Exception {
		
		int serverPort = DEFAULT_PORT;
		
		if(args.length >= 1) {
			try {
				serverPort = Integer.parseInt(args[0]);
			} catch (NumberFormatException e) {
				e.printStackTrace();
			}
		}
		new Main(serverPort);	
	}
}
