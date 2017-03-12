module.exports = async function (context) {
    console.log("headers=", JSON.stringify(context.request.headers));
    console.log("body=", JSON.stringify(context.request.body));

    return {
        status: 200,
        body: "Hello, world !\n"
    };
}
