module.exports = async function (context) {
  // var splitStringArray = context.request.body["sentence"].split(" ");

  return {
    status: 200,
    body: context.request.body,
  };
};
