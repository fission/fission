<?php

namespace PHPEnv;

use Monolog\Logger;
use Monolog\Handler\StreamHandler;
use Zend\Diactoros\ServerRequest;
use Zend\Diactoros\Response;
use Zend\Diactoros\Server as ZendServer;

class Server {
    private $server;

    public function __construct(){
        $codepath = '/userfunc/user';

        //if i have a local function i use it
        $devcodepath = '/app/userfuncdev.php';
        if(file_exists($devcodepath))
            $codepath = $devcodepath;

        $logger = new Logger("Function");
        $logger->pushHandler(new StreamHandler('php://stdout', Logger::DEBUG));

        $this->server = ZendServer::createServer(
            function (ServerRequest $request, Response $response) use ($logger, $codepath) {
                $path = parse_url($request->getUri(), PHP_URL_PATH);

                if($path == "/specialize" && $request->getMethod() == "POST"){
                    //Nothing to do
                    return new Response\EmptyResponse(201);
                }else{
                    if(!file_exists($codepath)){
                        $response = $response->withStatus(500);
                        $response->getBody()->write("Generic container: no requests supported");
                        return $response;
                    }

                    ob_start();
                    include($codepath);
                    //If the function as an handler class it will be called with request, response and logger
                    if(function_exists("handler")){
                        ob_end_clean();
                        return handler(array("request"=>$request, "response"=>$response,"logger"=>$logger));
                    }
                    //php code didn't have handler function, i will return the content
                    $bodyRowContent = ob_get_contents();
                    ob_end_clean();
                    return $response->getBody()->write($bodyRowContent);
                }
            },
            $_SERVER,
            $_GET,
            $_POST,
            $_COOKIE,
            $_FILES
        );
    }
    
    public function run(){
        $this->server->listen();
    }
}
?>