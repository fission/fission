module.exports = async (context) => {
  var splitStringArray = context.request.split(" ");

  return {
    status: 200,
    body: splitStringArray.length,
  };
};
