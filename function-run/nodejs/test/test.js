module.exports = function (context, callback) {
    console.log("headers=", JSON.stringify(context.request.headers));
    console.log("body=", JSON.stringify(context.request.body));

    callback(200, "Hello, world !\n");
}
