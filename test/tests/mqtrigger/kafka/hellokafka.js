
module.exports = async function (context) {
    console.log(context.request.body);
    console.log("z-custom-name: " + context.request.headers['z-custom-name']);
    console.log("x-fission-function-name: " + context.request.headers['x-fission-function-name']);
    let obj = context.request.body;
    let headers = context.request.headers;
    return {
        status: 200,
	headers: headers,
        body: obj
    };
}
