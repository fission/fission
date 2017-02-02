<?php
use \Psr\Http\Message\ResponseInterface;
use \Psr\Http\Message\ServerRequestInterface;
use \Psr\Log\LoggerInterface;

function sendError($response,$message){
    $response = $response->withStatus(500);
    $response->getBody()->write($message);
    return $response;
}

function handler($context){
    /** @var ResponseInterface $response */
    $response = $context["response"];
    /** @var ServerRequestInterface $request */
    $request = $context["request"];
    /** @var LoggerInterface $logger */
    $logger = $context["logger"];
    
    $logger->debug("Request : ",$request->getParsedBody());
    if($request->getMethod() != "POST")
        return sendError($response,"You must use POST method");

    $body = $request->getParsedBody();
    if(!isset($body["currency"]))
        return sendError($response,"'currency' is not present in the POST request");

    $allowedCurrency = ["ltc","btc"];
    if(!in_array($body["currency"],$allowedCurrency))
        return sendError($response,"'currency' is non allowed. Use one of them : ".implode(",",$allowedCurrency));

    $curl = curl_init();
    curl_setopt_array($curl, array(
        CURLOPT_RETURNTRANSFER => 1,
        CURLOPT_URL => 'https://api.cryptonator.com/api/ticker/'.$body["currency"].'-usd'
    ));
    $result = curl_exec($curl);
    curl_close($curl);

    $result = json_decode($result,true);
    if($result){
        $logger->debug("Response API",$result);
        $response->getBody()->write(json_encode(array("text"=>sprintf("%s-USB = %02f",$body["currency"],$result["ticker"]["price"]))));
    }else
        return sendError($response,"Cryptonator API not available");

}
