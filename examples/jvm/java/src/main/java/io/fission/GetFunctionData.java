package io.fission;

import org.springframework.http.RequestEntity;
import org.springframework.http.ResponseEntity;

import io.fission.Function;
import io.fission.Context;

public class GetFunctionData implements Function {

	@Override
	public ResponseEntity<?> call(RequestEntity req, Context context) {
		return ResponseEntity.ok(req.getBody());
	}

}
