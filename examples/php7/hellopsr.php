<?php
function handler(\Psr\Http\Message\ServerRequestInterface $request, \Psr\Http\Message\ResponseInterface $response,\Psr\Log\LoggerInterface $logger){
    $response->getBody()->write("Hello from handler PHP");
    $logger->warning("Hello logger");
}