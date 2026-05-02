module.exports = async function(context) {
    return {
        status: 400,
        body: "intentional error response\n"
    };
}
