<?php
$codepath = '/userfunc/user';

$path = parse_url($_SERVER["REQUEST_URI"], PHP_URL_PATH);

if($path == "/specialize" && $_SERVER["REQUEST_METHOD"] == "POST"){
    //nothing todo 
}else if($path == "/"){
    if(!file_exists($codepath)){
        header($_SERVER['SERVER_PROTOCOL'] . ' 500 Internal Server Error', true, 500);
        echo "Generic container: no requests supported";
        exit();
    }

    ob_start();
    include($codepath);
    $reponse = ob_get_contents();
    ob_end_clean();
    
    echo $reponse;
}
?>