package io.fission;

import java.io.File;
import java.io.IOException;
import java.net.MalformedURLException;
import java.net.URI;
import java.net.URL;
import java.net.URLClassLoader;
import java.util.Enumeration;
import java.util.jar.JarEntry;
import java.util.jar.JarFile;
import java.util.logging.Level;
import java.util.logging.Logger;
import java.util.stream.Collectors;
import static java.util.Collections.singletonList;

import javax.servlet.http.HttpServletRequest;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.EnableAutoConfiguration;
import org.springframework.http.HttpMethod;
import org.springframework.http.RequestEntity;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestMethod;
import org.springframework.web.bind.annotation.RestController;

import org.springframework.util.LinkedMultiValueMap;
import org.springframework.util.MultiValueMap;

@RestController
@EnableAutoConfiguration
public class Server {

	private Function fn;

	private static final int CLASS_LENGTH = 6;

	private static Logger logger = Logger.getGlobal();

	@RequestMapping(value = "/", method = { RequestMethod.GET, RequestMethod.POST, RequestMethod.DELETE, RequestMethod.PUT })
	ResponseEntity<Object> home(HttpServletRequest request) {

		String body;
		// Converting header in multimap
		try {
				MultiValueMap<String, String> headers = new LinkedMultiValueMap<>();
				Enumeration headerNames = request.getHeaderNames();
				while (headerNames.hasMoreElements()) {
					String key = (String) headerNames.nextElement();
					String value = request.getHeader(key);
					headers.put(key, singletonList(value));
				 }

				//Extract the body from the request
				body = request.getReader().lines().collect(Collectors.joining(System.lineSeparator()));
				//Get the rawrequest
				RequestEntity<String> rawRequest = new RequestEntity<String>(body,headers,HttpMethod.resolve(request.getMethod()), new URI(request.getRequestURI()));

				if (fn == null) {
					return ResponseEntity.badRequest().body("Container not specialized");
				} else {
					return ((ResponseEntity<Object>) ((Function) fn).call(rawRequest, null));
				}
		} catch (Exception e) {
			e.printStackTrace();
			return ResponseEntity.badRequest().body(e.getMessage());
		}
	}

	@PostMapping(path = "/v2/specialize", consumes = "application/json")
	ResponseEntity<String> specialize(@RequestBody FunctionLoadRequest req) {
		long startTime = System.nanoTime();
		File file = new File(req.getFilepath());
		if (!file.exists()) {
			return ResponseEntity.badRequest().body("/userfunc/user not found");
		}

		String entryPoint = req.getFunctionName();
		logger.log(Level.INFO, "Entrypoint class:" + entryPoint);
		if (entryPoint == null) {
			return ResponseEntity.badRequest().body("Entrypoint class is missing in the function");
		}

		JarFile jarFile = null;
		ClassLoader cl = null;
		try {

			jarFile = new JarFile(file);
			Enumeration<JarEntry> e = jarFile.entries();
			URL[] urls = { new URL("jar:file:" + file + "!/") };

			// TODO Check if the classloading can be improved for ex. use something like:
			// Thread.currentThread().setContextClassLoader(cl);
			if (this.getClass().getClassLoader() == null) {
				cl = URLClassLoader.newInstance(urls);
			} else {
				cl = URLClassLoader.newInstance(urls, this.getClass().getClassLoader());
			}

			if (cl == null) {
				return ResponseEntity.status(500).body("Failed to initialize the classloader");
			}

			// Load all dependent classes from libraries etc.
			while (e.hasMoreElements()) {
				JarEntry je = e.nextElement();
				if (je.isDirectory() || !je.getName().endsWith(".class")) {
					continue;
				}
				String className = je.getName().substring(0, je.getName().length() - CLASS_LENGTH);
				className = className.replace('/', '.');
				cl.loadClass(className);
			}

			// Instantiate the function class
			fn = (Function) cl.loadClass(entryPoint).newInstance();

		} catch (MalformedURLException e) {
			e.printStackTrace();
			return ResponseEntity.badRequest().body("Error loading the Function class file");
		} catch (ClassNotFoundException e) {
			e.printStackTrace();
			return ResponseEntity.badRequest().body("Error loading Function or dependent class");
		} catch (InstantiationException e) {
			e.printStackTrace();
			return ResponseEntity.badRequest().body("Error creating a new instance of function class");
		} catch (IllegalAccessException e) {
			e.printStackTrace();
			return ResponseEntity.badRequest().body("Error creating a new instance of function class");
		} catch (IOException e) {
			e.printStackTrace();
			return ResponseEntity.badRequest().body("Error reading the JAR file");
		} finally {
			try {
				// cl.close();
				jarFile.close();
			} catch (IOException e) {
				e.printStackTrace();
				return ResponseEntity.badRequest().body("Error closing the file while loading the class");
			}
		}
		long elapsedTime = System.nanoTime() - startTime;
		logger.log(Level.INFO, "Specialize call done in: " + elapsedTime / 1000000 + " ms");
		return ResponseEntity.ok("Done");
	}

	public static void main(String[] args) throws Exception {
		SpringApplication.run(Server.class, args);
	}

}