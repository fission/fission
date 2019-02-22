<?php

namespace PHPEnv;

use Monolog\Logger;
use Monolog\Handler\StreamHandler;
use Zend\Diactoros\ServerRequest;
use Zend\Diactoros\Response;
use Zend\Diactoros\Server as ZendServer;

class Server {

    private const V1_CODEPATH = '/userfunc/user';

    private const V1_USER_FUNCTION = 'handler';

    private const HANDLER_DIVIDER = '.';

    private $server;

    private $userFunction;

    public function __construct(){

        //if i have a local function i use it
        $devcodepath = '/app/userfuncdev.php';
        if(file_exists($devcodepath))
            $this->userFunction = $devcodepath;

        $logger = new Logger("Function");
        $logger->pushHandler(new StreamHandler('php://stdout', Logger::DEBUG));

        $this->server = ZendServer::createServer(
            function (ServerRequest $request, Response $response) use ($logger) {
                $path = parse_url($request->getUri(), PHP_URL_PATH);

                if('/specialize' === $path && 'POST' === $request->getMethod()) {
                    $this->load();
                }
                elseif ('/v2/specialize' === $path && 'POST' === $request->getMethod()) {
                    $this->loadV2($request);
                }
                else {
                    $this->execute($request, $response, $logger);
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

    private function load()
    {
        include(self::V1_CODEPATH);
        $this->userFunction = self::V1_USER_FUNCTION;
    }

    private function loadV2($request)
    {
        $body = json_decode($request->getBody()->getContents(), true);
        $filepath = $body['filepath'];
        $handler = explode(self::HANDLER_DIVIDER, $body['functionName']);
        list ($moduleName, $funcName) = $handler[1];
        if (true === is_dir($filepath)) {
            require $filepath . DIRECTORY_SEPARATOR . $moduleName;

        } else {
            require $filepath;
        }
        $this->userFunction = $funcName;
    }

    private function execute($request, $response, $logger)
    {
        if (null === $this->userFunction) {
            $response = $response->withStatus(500);
            $response->getBody()->write('Generic container: no requests supported');
            return $response;
        }
        ob_start();
        include(self::V1_CODEPATH);
        //If the function as an handler class it will be called with request, response and logger
        if(function_exists($this->userFunction)) {
            ob_end_clean();
            ${$this->userFunction}([$request, $response, $logger]);
        }
        //backwards compatibility: php code didn't have userFunction, i will return the content
        $bodyRowContent = ob_get_contents();
        ob_end_clean();
        return $response->getBody()->write($bodyRowContent);
    }
}
?>
