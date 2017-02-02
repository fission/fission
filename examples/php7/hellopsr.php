<?php
function handler($context){
    /** @var \Psr\Http\Message\ResponseInterface $response */
    $response = $context["response"];
    /** @var \Psr\Http\Message\ServerRequestInterface $request */
    $request = $context["request"];
    /** @var \Psr\Log\LoggerInterface $logger */
    $logger = $context["logger"];
    
    $response->getBody()->write("Hello from handler PHP");
    $logger->warning("Hello logger");
}