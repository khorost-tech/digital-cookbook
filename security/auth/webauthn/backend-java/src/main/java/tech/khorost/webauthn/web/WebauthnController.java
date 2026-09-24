package tech.khorost.webauthn.web;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import tech.khorost.webauthn.service.WebauthnService;
import tech.khorost.webauthn.web.dto.Dtos;

/**
 * HTTP-контракт, совместимый с backend-go (Task 4): /register/begin|finish,
 * /login/begin|finish. finish-endpoint'ы принимают username через query-параметр,
 * как и в Go-версии.
 */
@RestController
public class WebauthnController {

    private final WebauthnService service;

    public WebauthnController(WebauthnService service) {
        this.service = service;
    }

    @PostMapping("/register/begin")
    public Dtos.CreationOptionsResponse registerBegin(@RequestBody(required = false) Dtos.UsernameRequest req) {
        return service.beginRegistration(req == null ? null : req.username());
    }

    @PostMapping("/register/finish")
    public Dtos.StatusResponse registerFinish(
            @RequestParam(required = false) String username,
            @RequestBody Dtos.AttestationRequestBody body) {
        service.finishRegistration(username, body);
        return new Dtos.StatusResponse("ok");
    }

    @PostMapping("/login/begin")
    public Dtos.RequestOptionsResponse loginBegin(@RequestBody(required = false) Dtos.UsernameRequest req) {
        return service.beginLogin(req == null ? null : req.username());
    }

    @PostMapping("/login/finish")
    public Dtos.StatusResponse loginFinish(
            @RequestParam(required = false) String username,
            @RequestBody Dtos.AssertionRequestBody body) {
        service.finishLogin(username, body);
        return new Dtos.StatusResponse("ok");
    }

    @ExceptionHandler(BadRequestException.class)
    public ResponseEntity<Dtos.ErrorResponse> handleBadRequest(BadRequestException e) {
        return ResponseEntity.status(HttpStatus.BAD_REQUEST).body(new Dtos.ErrorResponse(e.getMessage()));
    }

    @ExceptionHandler(UnauthorizedException.class)
    public ResponseEntity<Dtos.ErrorResponse> handleUnauthorized(UnauthorizedException e) {
        return ResponseEntity.status(HttpStatus.UNAUTHORIZED).body(new Dtos.ErrorResponse(e.getMessage()));
    }
}
